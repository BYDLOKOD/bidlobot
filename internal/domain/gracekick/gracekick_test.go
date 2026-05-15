package gracekick_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/domain/cleanup"
	"github.com/veschin/bidlobot/internal/domain/gracekick"
	"github.com/veschin/bidlobot/internal/domain/membership"
)

// ---- fakes -------------------------------------------------------------

type memStore struct{ recs map[int64]gracekick.Record }

func newMemStore() *memStore { return &memStore{recs: map[int64]gracekick.Record{}} }

func (s *memStore) Put(_ context.Context, r gracekick.Record) error {
	s.recs[r.UserID] = r
	return nil
}

func (s *memStore) ListByChat(_ context.Context, _ int64) ([]gracekick.Record, error) {
	out := make([]gracekick.Record, 0, len(s.recs))
	for _, r := range s.recs {
		out = append(out, r)
	}
	return out, nil
}

func (s *memStore) Delete(_ context.Context, _, userID int64) error {
	delete(s.recs, userID)
	return nil
}

type fakeKick struct{ kicked []int64 }

func (f *fakeKick) ExecuteCleanup(_ context.Context, _ int64, cands []membership.Member, _ func(int, int, cleanup.ExecutionEntry)) (*cleanup.Report, error) {
	for _, m := range cands {
		f.kicked = append(f.kicked, m.UserID)
	}
	return &cleanup.Report{Total: len(cands), Kicked: len(cands)}, nil
}

type fakeMembers struct {
	m   map[int64]*membership.Member
	err error
}

func (f *fakeMembers) GetMember(_ context.Context, userID, _ int64) (*membership.Member, error) {
	if f.err != nil {
		return nil, f.err
	}
	if mm, ok := f.m[userID]; ok {
		return mm, nil
	}
	return nil, membership.ErrNotFound
}

type fakeOut struct {
	sent []string
	err  error
}

func (f *fakeOut) SendMessage(_ context.Context, p *telego.SendMessageParams) (*telego.Message, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.sent = append(f.sent, p.Text)
	return &telego.Message{MessageID: 1}, nil
}

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

const absChat = int64(555)

func member(id int64) membership.Member {
	return membership.Member{UserID: id, AbsChatID: absChat, Status: membership.StatusMember}
}

// liveMember is a membership record the bot "sees" at promote/sweep time.
func liveMember(id int64, lastMsg, lastReact time.Time, status membership.Status, bot bool) *membership.Member {
	return &membership.Member{
		UserID: id, AbsChatID: absChat, Status: status, IsBot: bot,
		LastMessageAt: lastMsg, LastReactionAt: lastReact,
	}
}

func newSvc(store gracekick.Store, kick *fakeKick, mem *fakeMembers, out *fakeOut, cfg gracekick.Config) *gracekick.Service {
	return gracekick.NewService(store, kick, mem, out, cfg, testLog())
}

// ---- Seed --------------------------------------------------------------

func TestSeedQueuesMembersIdempotently(t *testing.T) {
	store := newMemStore()
	svc := newSvc(store, &fakeKick{}, &fakeMembers{}, &fakeOut{}, gracekick.Config{})
	now := time.Now().UTC()

	n, err := svc.Seed(context.Background(), absChat, []membership.Member{member(1), member(2), member(3)}, now)
	if err != nil || n != 3 {
		t.Fatalf("first seed: n=%d err=%v", n, err)
	}
	if len(store.recs) != 3 || store.recs[1].State != gracekick.StateQueued {
		t.Fatalf("expected 3 queued, got %+v", store.recs)
	}
	// Re-seed overlapping set: only the new one is added.
	n, err = svc.Seed(context.Background(), absChat, []membership.Member{member(2), member(3), member(4)}, now)
	if err != nil || n != 1 {
		t.Fatalf("re-seed must add only the new member: n=%d err=%v", n, err)
	}
	if len(store.recs) != 4 {
		t.Fatalf("want 4 records after idempotent re-seed, got %d", len(store.recs))
	}
}

func TestCampaignSizeAndCancel(t *testing.T) {
	store := newMemStore()
	svc := newSvc(store, &fakeKick{}, &fakeMembers{}, &fakeOut{}, gracekick.Config{})
	now := time.Now().UTC()
	_, _ = svc.Seed(context.Background(), absChat, []membership.Member{member(1), member(2)}, now)

	if n, _ := svc.CampaignSize(context.Background(), absChat); n != 2 {
		t.Fatalf("CampaignSize want 2, got %d", n)
	}
	dropped, err := svc.Cancel(context.Background(), absChat)
	if err != nil || dropped != 2 {
		t.Fatalf("Cancel want 2 dropped, got %d err=%v", dropped, err)
	}
	if n, _ := svc.CampaignSize(context.Background(), absChat); n != 0 {
		t.Fatalf("after Cancel size must be 0, got %d", n)
	}
}

func TestRunDailyNoCampaignIsNoop(t *testing.T) {
	out := &fakeOut{}
	svc := newSvc(newMemStore(), &fakeKick{}, &fakeMembers{}, out, gracekick.Config{})
	sum, err := svc.RunDaily(context.Background(), absChat, time.Now().UTC())
	if err != nil || sum != (gracekick.Summary{}) || len(out.sent) != 0 {
		t.Fatalf("empty campaign must be a no-op, got sum=%+v err=%v sent=%d", sum, err, len(out.sent))
	}
}

// ---- promote queued -> tagged -----------------------------------------

func TestRunDailyPromotesBatchAndTags(t *testing.T) {
	store := newMemStore()
	now := time.Now().UTC()
	seedAt := now.Add(-24 * time.Hour)
	mem := &fakeMembers{m: map[int64]*membership.Member{}}
	for i := int64(1); i <= 5; i++ {
		store.recs[i] = gracekick.Record{AbsChatID: absChat, UserID: i, State: gracekick.StateQueued, SeededAt: seedAt}
		// silent since seeding (activity well before seedAt)
		mem.m[i] = liveMember(i, seedAt.Add(-100*time.Hour), time.Time{}, membership.StatusMember, false)
	}
	out := &fakeOut{}
	svc := newSvc(store, &fakeKick{}, mem, out, gracekick.Config{Grace: 72 * time.Hour, Batch: 3})

	sum, err := svc.RunDaily(context.Background(), absChat, now)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Tagged != 3 {
		t.Fatalf("batch cap 3: want 3 tagged, got %d", sum.Tagged)
	}
	if len(out.sent) != 1 {
		t.Fatalf("exactly one public message, got %d", len(out.sent))
	}
	tagged := 0
	for _, r := range store.recs {
		if r.State == gracekick.StateTagged {
			tagged++
			if !r.GraceDeadline.Equal(now.Truncate(time.Second).Add(72 * time.Hour)) {
				t.Fatalf("deadline must be truncated(now)+grace, got %v", r.GraceDeadline)
			}
		}
	}
	if tagged != 3 {
		t.Fatalf("want 3 records flipped to tagged, got %d", tagged)
	}
}

func TestPromoteSkipsCameBackSinceSeed(t *testing.T) {
	store := newMemStore()
	now := time.Now().UTC()
	seedAt := now.Add(-48 * time.Hour)
	store.recs[1] = gracekick.Record{AbsChatID: absChat, UserID: 1, State: gracekick.StateQueued, SeededAt: seedAt}
	store.recs[2] = gracekick.Record{AbsChatID: absChat, UserID: 2, State: gracekick.StateQueued, SeededAt: seedAt}
	mem := &fakeMembers{m: map[int64]*membership.Member{
		1: liveMember(1, seedAt.Add(2*time.Hour), time.Time{}, membership.StatusMember, false), // wrote after seed -> spared
		2: liveMember(2, seedAt.Add(-200*time.Hour), time.Time{}, membership.StatusMember, false),
	}}
	out := &fakeOut{}
	svc := newSvc(store, &fakeKick{}, mem, out, gracekick.Config{Batch: 15})

	sum, err := svc.RunDaily(context.Background(), absChat, now)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Saved != 1 {
		t.Fatalf("member active since seed must be spared (Saved=1), got %d", sum.Saved)
	}
	if _, ok := store.recs[1]; ok {
		t.Fatal("spared member's record must be dropped, never tagged")
	}
	if sum.Tagged != 1 || store.recs[2].State != gracekick.StateTagged {
		t.Fatalf("the still-silent member must be tagged, got tagged=%d", sum.Tagged)
	}
}

func TestPromoteDropsAdminLeftBotAndDefersUnreadable(t *testing.T) {
	store := newMemStore()
	now := time.Now().UTC()
	seedAt := now.Add(-24 * time.Hour)
	for _, id := range []int64{1, 2, 3, 4} {
		store.recs[id] = gracekick.Record{AbsChatID: absChat, UserID: id, State: gracekick.StateQueued, SeededAt: seedAt}
	}
	mem := &fakeMembers{m: map[int64]*membership.Member{
		1: liveMember(1, time.Time{}, time.Time{}, membership.StatusAdministrator, false), // became admin
		2: liveMember(2, time.Time{}, time.Time{}, membership.StatusLeft, false),          // left
		3: liveMember(3, time.Time{}, time.Time{}, membership.StatusMember, true),         // bot
		// id 4: absent from map -> GetMember ErrNotFound -> undeterminable -> deferred
	}}
	out := &fakeOut{}
	svc := newSvc(store, &fakeKick{}, mem, out, gracekick.Config{Batch: 15})

	sum, err := svc.RunDaily(context.Background(), absChat, now)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Tagged != 0 || len(out.sent) != 0 {
		t.Fatalf("admin/left/bot must not be tagged; unreadable deferred. tagged=%d sent=%d", sum.Tagged, len(out.sent))
	}
	for _, id := range []int64{1, 2, 3} {
		if _, ok := store.recs[id]; ok {
			t.Fatalf("invalid target %d must be dropped from the campaign", id)
		}
	}
	if r, ok := store.recs[4]; !ok || r.State != gracekick.StateQueued {
		t.Fatal("unreadable member must stay queued (deferred), not dropped/tagged")
	}
}

func TestAnnounceFailureKeepsQueued(t *testing.T) {
	store := newMemStore()
	now := time.Now().UTC()
	seedAt := now.Add(-24 * time.Hour)
	store.recs[1] = gracekick.Record{AbsChatID: absChat, UserID: 1, State: gracekick.StateQueued, SeededAt: seedAt}
	mem := &fakeMembers{m: map[int64]*membership.Member{
		1: liveMember(1, seedAt.Add(-100*time.Hour), time.Time{}, membership.StatusMember, false),
	}}
	out := &fakeOut{err: errors.New("telegram down")}
	svc := newSvc(store, &fakeKick{}, mem, out, gracekick.Config{Batch: 15})

	sum, err := svc.RunDaily(context.Background(), absChat, now)
	if err == nil {
		t.Fatal("announce failure must surface as error")
	}
	if sum.Tagged != 0 || store.recs[1].State != gracekick.StateQueued {
		t.Fatal("a member must NOT be marked tagged if the warning never posted")
	}
}

// ---- sweep tagged -----------------------------------------------------

func taggedRec(uid int64, taggedAt, deadline time.Time) gracekick.Record {
	return gracekick.Record{
		AbsChatID: absChat, UserID: uid, State: gracekick.StateTagged,
		SeededAt: taggedAt.Add(-24 * time.Hour), TaggedAt: taggedAt, GraceDeadline: deadline,
	}
}

func TestSweepSavesOnActivityAfterTag(t *testing.T) {
	store := newMemStore()
	now := time.Now().UTC()
	tagged := now.Add(-96 * time.Hour)
	store.recs[1] = taggedRec(1, tagged, now.Add(-24*time.Hour)) // deadline passed
	store.recs[2] = taggedRec(2, tagged, now.Add(-24*time.Hour))
	mem := &fakeMembers{m: map[int64]*membership.Member{
		1: liveMember(1, tagged.Add(2*time.Hour), time.Time{}, membership.StatusMember, false), // wrote after tag
		2: liveMember(2, time.Time{}, tagged.Add(time.Hour), membership.StatusMember, false),   // reacted after tag
	}}
	kick := &fakeKick{}
	svc := newSvc(store, kick, mem, &fakeOut{}, gracekick.Config{Batch: 15})

	sum, err := svc.RunDaily(context.Background(), absChat, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(kick.kicked) != 0 || sum.Saved != 2 {
		t.Fatalf("message OR reaction after tag must spare: kicked=%v saved=%d", kick.kicked, sum.Saved)
	}
	if len(store.recs) != 0 {
		t.Fatal("saved members' records must be cleared")
	}
}

// B1/S1 boundary: Telegram timestamps are second-granular. A member who
// acts in the very second they were tagged/seeded must NOT be kicked or
// publicly tagged (inclusive >= comparison).
func TestBoundarySecondActivityIsNotKickedNorTagged(t *testing.T) {
	now := time.Now().UTC()

	// Sweep: LastMessageAt == TaggedAt exactly -> saved, not kicked.
	store := newMemStore()
	tagged := now.Add(-96 * time.Hour).Truncate(time.Second)
	store.recs[1] = taggedRec(1, tagged, now.Add(-time.Hour))
	mem := &fakeMembers{m: map[int64]*membership.Member{
		1: liveMember(1, tagged, time.Time{}, membership.StatusMember, false),
	}}
	kick := &fakeKick{}
	svc := newSvc(store, kick, mem, &fakeOut{}, gracekick.Config{Batch: 15})
	sum, err := svc.RunDaily(context.Background(), absChat, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(kick.kicked) != 0 || sum.Saved != 1 {
		t.Fatalf("same-second-as-tag activity must save, not kick: kicked=%v saved=%d", kick.kicked, sum.Saved)
	}

	// Promote: LastReactionAt == SeededAt exactly -> spared, not tagged.
	store2 := newMemStore()
	seedAt := now.Add(-24 * time.Hour).Truncate(time.Second)
	store2.recs[9] = gracekick.Record{AbsChatID: absChat, UserID: 9, State: gracekick.StateQueued, SeededAt: seedAt}
	mem2 := &fakeMembers{m: map[int64]*membership.Member{
		9: liveMember(9, time.Time{}, seedAt, membership.StatusMember, false),
	}}
	out := &fakeOut{}
	svc2 := newSvc(store2, &fakeKick{}, mem2, out, gracekick.Config{Batch: 15})
	sum2, err := svc2.RunDaily(context.Background(), absChat, now)
	if err != nil {
		t.Fatal(err)
	}
	if sum2.Tagged != 0 || len(out.sent) != 0 || sum2.Saved != 1 {
		t.Fatalf("same-second-as-seed activity must spare, not tag: tagged=%d sent=%d saved=%d",
			sum2.Tagged, len(out.sent), sum2.Saved)
	}
}

func TestSweepKicksStillSilentPastDeadline(t *testing.T) {
	store := newMemStore()
	now := time.Now().UTC()
	tagged := now.Add(-96 * time.Hour)
	store.recs[1] = taggedRec(1, tagged, now.Add(-time.Hour))
	mem := &fakeMembers{m: map[int64]*membership.Member{
		1: liveMember(1, tagged.Add(-200*time.Hour), time.Time{}, membership.StatusMember, false), // silent since tag
	}}
	kick := &fakeKick{}
	svc := newSvc(store, kick, mem, &fakeOut{}, gracekick.Config{Batch: 15})

	sum, err := svc.RunDaily(context.Background(), absChat, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(kick.kicked) != 1 || kick.kicked[0] != 1 || sum.Kicked != 1 {
		t.Fatalf("still-silent past deadline must be kicked, kicked=%v", kick.kicked)
	}
	if _, ok := store.recs[1]; ok {
		t.Fatal("kicked member's record must be cleared")
	}
}

func TestSweepDefersWhenLiveRecordUnreadable(t *testing.T) {
	store := newMemStore()
	now := time.Now().UTC()
	store.recs[1] = taggedRec(1, now.Add(-96*time.Hour), now.Add(-24*time.Hour))
	kick := &fakeKick{}
	svc := newSvc(store, kick, &fakeMembers{err: errors.New("bbolt read failed")}, &fakeOut{}, gracekick.Config{Batch: 15})

	sum, err := svc.RunDaily(context.Background(), absChat, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(kick.kicked) != 0 || sum.Kicked != 0 {
		t.Fatalf("never kick on uncertainty, kicked=%v", kick.kicked)
	}
	if _, ok := store.recs[1]; !ok {
		t.Fatal("undeterminable member's ticket must be retained")
	}
}

func TestSweepLeavesUnexpiredTaggedAlone(t *testing.T) {
	store := newMemStore()
	now := time.Now().UTC()
	store.recs[1] = taggedRec(1, now.Add(-time.Hour), now.Add(71*time.Hour)) // grace still running
	kick := &fakeKick{}
	out := &fakeOut{}
	svc := newSvc(store, kick, &fakeMembers{}, out, gracekick.Config{Batch: 15})

	sum, err := svc.RunDaily(context.Background(), absChat, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(kick.kicked) != 0 || sum != (gracekick.Summary{}) || len(out.sent) != 0 {
		t.Fatalf("an unexpired tagged record must be untouched, got sum=%+v kicked=%v", sum, kick.kicked)
	}
	if _, ok := store.recs[1]; !ok {
		t.Fatal("unexpired record must survive")
	}
}

// ---- announcement size safety (UTF-16) --------------------------------

func utf16Units(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func TestRunDailyTrimsOversizedAnnouncement(t *testing.T) {
	store := newMemStore()
	now := time.Now().UTC()
	seedAt := now.Add(-24 * time.Hour)
	astral := strings.Repeat("😀", 32) // 32 runes = 64 UTF-16 units
	mem := &fakeMembers{m: map[int64]*membership.Member{}}
	for i := int64(1); i <= 50; i++ {
		store.recs[i] = gracekick.Record{
			AbsChatID: absChat, UserID: i, FirstName: astral,
			State: gracekick.StateQueued, SeededAt: seedAt,
		}
		mem.m[i] = liveMember(i, seedAt.Add(-100*time.Hour), time.Time{}, membership.StatusMember, false)
	}
	out := &fakeOut{}
	svc := newSvc(store, &fakeKick{}, mem, out, gracekick.Config{Batch: 50})

	sum, err := svc.RunDaily(context.Background(), absChat, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.sent) != 1 {
		t.Fatalf("one message, got %d", len(out.sent))
	}
	if u := utf16Units(out.sent[0]); u >= 4096 {
		t.Fatalf("announcement must stay under 4096 UTF-16 units, got %d", u)
	}
	if sum.Tagged == 0 || sum.Tagged >= 50 {
		t.Fatalf("oversized batch must be trimmed (0<tagged<50), got %d", sum.Tagged)
	}
}

// ---- end-to-end lifecycle ---------------------------------------------

func TestFullLifecycleSeedTagGraceKickAndSave(t *testing.T) {
	store := newMemStore()
	mem := &fakeMembers{m: map[int64]*membership.Member{}}
	kick := &fakeKick{}
	out := &fakeOut{}
	svc := newSvc(store, kick, mem, out, gracekick.Config{Grace: 72 * time.Hour, Batch: 10})

	day0 := time.Now().UTC().Add(-10 * 24 * time.Hour)
	if n, err := svc.Seed(context.Background(), absChat, []membership.Member{member(1), member(2)}, day0); err != nil || n != 2 {
		t.Fatalf("seed: n=%d err=%v", n, err)
	}
	// both silent since seeding
	mem.m[1] = liveMember(1, day0.Add(-100*time.Hour), time.Time{}, membership.StatusMember, false)
	mem.m[2] = liveMember(2, day0.Add(-100*time.Hour), time.Time{}, membership.StatusMember, false)

	// Day 1: promote+tag both.
	day1 := day0.Add(24 * time.Hour)
	if sum, err := svc.RunDaily(context.Background(), absChat, day1); err != nil || sum.Tagged != 2 {
		t.Fatalf("day1 tag: sum=%+v err=%v", sum, err)
	}
	// User 1 reacts after being tagged; user 2 stays silent.
	mem.m[1] = liveMember(1, day0.Add(-100*time.Hour), day1.Add(time.Hour), membership.StatusMember, false)

	// Day 5: grace (3d) expired -> 1 saved, 2 kicked.
	day5 := day1.Add(4 * 24 * time.Hour)
	sum, err := svc.RunDaily(context.Background(), absChat, day5)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Saved != 1 || sum.Kicked != 1 || len(kick.kicked) != 1 || kick.kicked[0] != 2 {
		t.Fatalf("want user1 saved, user2 kicked; got saved=%d kicked=%v", sum.Saved, kick.kicked)
	}
	if n, _ := svc.CampaignSize(context.Background(), absChat); n != 0 {
		t.Fatalf("campaign must end empty, got %d", n)
	}
}
