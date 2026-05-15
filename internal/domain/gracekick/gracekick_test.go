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

type memStore struct {
	recs map[int64]gracekick.Record // userID -> record (single test chat)
}

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

type fakePrev struct {
	preview  *cleanup.Preview
	resolved map[int64]cleanup.ResolvedMember // overrides per user
}

func (f *fakePrev) PreviewInactive(_ context.Context, _ int64, _ time.Duration, _ time.Time) (*cleanup.Preview, error) {
	return f.preview, nil
}

func (f *fakePrev) ResolveIdentities(_ context.Context, _ int64, in []membership.Member, _ int) []cleanup.ResolvedMember {
	out := make([]cleanup.ResolvedMember, 0, len(in))
	for _, m := range in {
		if rm, ok := f.resolved[m.UserID]; ok {
			rm.Member = m
			out = append(out, rm)
			continue
		}
		out = append(out, cleanup.ResolvedMember{
			Member: m, Resolved: true, Present: true,
		})
	}
	return out
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

func testLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

const absChat = int64(555)

func member(id int64) membership.Member {
	return membership.Member{UserID: id, AbsChatID: absChat, Status: membership.StatusMember}
}

func newSvc(t *testing.T, prev *fakePrev, kick *fakeKick, mem *fakeMembers, out *fakeOut, store gracekick.Store, cfg gracekick.Config) *gracekick.Service {
	t.Helper()
	return gracekick.NewService(store, prev, kick, mem, out, cfg, testLog())
}

// ---- tag phase ---------------------------------------------------------

func TestRunDailyTagsProvenStaleOnlyNeverNoEvidence(t *testing.T) {
	now := time.Now().UTC()
	prev := &fakePrev{preview: &cleanup.Preview{
		Candidates: []membership.Member{member(1), member(2)},
		NoEvidence: []membership.Member{member(99)}, // MUST never be tagged
	}}
	store := newMemStore()
	out := &fakeOut{}
	svc := newSvc(t, prev, &fakeKick{}, &fakeMembers{}, out, store, gracekick.Config{Grace: 72 * time.Hour, Batch: 15})

	sum, err := svc.RunDaily(context.Background(), absChat, now)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Tagged != 2 {
		t.Fatalf("want 2 tagged, got %d", sum.Tagged)
	}
	if _, ok := store.recs[99]; ok {
		t.Fatal("a NoEvidence member was tagged - this is the unacceptable case")
	}
	if len(out.sent) != 1 {
		t.Fatalf("want exactly one public message, got %d", len(out.sent))
	}
	r := store.recs[1]
	// TaggedAt is truncated to whole seconds (to match second-granular
	// Telegram activity timestamps), so the deadline is base+grace where
	// base = now truncated to the second.
	wantDeadline := now.Truncate(time.Second).Add(72 * time.Hour)
	if !r.GraceDeadline.Equal(wantDeadline) {
		t.Fatalf("grace deadline must be truncated(now)+grace, got %v want %v", r.GraceDeadline, wantDeadline)
	}
	if !r.TaggedAt.Equal(now.Truncate(time.Second)) {
		t.Fatalf("TaggedAt must be truncated to the second, got %v", r.TaggedAt)
	}
}

func TestRunDailyBatchCap(t *testing.T) {
	now := time.Now().UTC()
	prev := &fakePrev{preview: &cleanup.Preview{
		Candidates: []membership.Member{member(1), member(2), member(3), member(4), member(5)},
	}}
	store := newMemStore()
	svc := newSvc(t, prev, &fakeKick{}, &fakeMembers{}, &fakeOut{}, store, gracekick.Config{Batch: 2})

	sum, err := svc.RunDaily(context.Background(), absChat, now)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Tagged != 2 || len(store.recs) != 2 {
		t.Fatalf("batch cap 2 not honoured: tagged=%d stored=%d", sum.Tagged, len(store.recs))
	}
}

func TestRunDailySkipsMembersAlreadyHoldingAnOpenTicket(t *testing.T) {
	now := time.Now().UTC()
	prev := &fakePrev{preview: &cleanup.Preview{
		Candidates: []membership.Member{member(1), member(2)},
	}}
	store := newMemStore()
	// u1 already tagged yesterday, deadline still in the future.
	store.recs[1] = gracekick.Record{
		AbsChatID: absChat, UserID: 1,
		TaggedAt: now.Add(-24 * time.Hour), GraceDeadline: now.Add(48 * time.Hour),
	}
	out := &fakeOut{}
	svc := newSvc(t, prev, &fakeKick{}, &fakeMembers{}, out, store, gracekick.Config{Grace: 72 * time.Hour, Batch: 15})

	sum, err := svc.RunDaily(context.Background(), absChat, now)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Tagged != 1 {
		t.Fatalf("only the un-ticketed member should be freshly tagged, got %d", sum.Tagged)
	}
	if got := store.recs[1].GraceDeadline; !got.Equal(now.Add(48 * time.Hour)) {
		t.Fatalf("existing open ticket must not be refreshed/clobbered, got %v", got)
	}
}

func TestRunDailySkipsProtectedAndLeftResolved(t *testing.T) {
	now := time.Now().UTC()
	prev := &fakePrev{
		preview: &cleanup.Preview{Candidates: []membership.Member{member(1), member(2), member(3)}},
		resolved: map[int64]cleanup.ResolvedMember{
			1: {Resolved: true, Present: true, Protected: true}, // admin/bot
			2: {Resolved: true, Present: false},                 // already left
			3: {Resolved: true, Present: true},                  // the only valid one
		},
	}
	store := newMemStore()
	svc := newSvc(t, prev, &fakeKick{}, &fakeMembers{}, &fakeOut{}, store, gracekick.Config{Batch: 15})

	sum, err := svc.RunDaily(context.Background(), absChat, now)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Tagged != 1 {
		t.Fatalf("only user 3 should be tagged, got %d", sum.Tagged)
	}
	if _, ok := store.recs[3]; !ok {
		t.Fatal("user 3 must have a ticket")
	}
}

func TestAnnounceFailurePersistsNoTickets(t *testing.T) {
	now := time.Now().UTC()
	prev := &fakePrev{preview: &cleanup.Preview{Candidates: []membership.Member{member(1)}}}
	store := newMemStore()
	out := &fakeOut{err: errors.New("telegram down")}
	svc := newSvc(t, prev, &fakeKick{}, &fakeMembers{}, out, store, gracekick.Config{Batch: 15})

	sum, err := svc.RunDaily(context.Background(), absChat, now)
	if err == nil {
		t.Fatal("announce failure must surface as an error")
	}
	if sum.Tagged != 0 || len(store.recs) != 0 {
		t.Fatal("no ticket may exist for a warning that never reached the chat (else silent kick in 3d)")
	}
}

// ---- sweep phase -------------------------------------------------------

func openTicket(store *memStore, uid int64, tagged, deadline time.Time) {
	store.recs[uid] = gracekick.Record{
		AbsChatID: absChat, UserID: uid, TaggedAt: tagged, GraceDeadline: deadline,
	}
}

func TestSweepSavesOnMessageAfterTag(t *testing.T) {
	now := time.Now().UTC()
	tagged := now.Add(-96 * time.Hour)
	store := newMemStore()
	openTicket(store, 1, tagged, now.Add(-24*time.Hour)) // deadline passed
	mem := &fakeMembers{m: map[int64]*membership.Member{
		1: {UserID: 1, AbsChatID: absChat, LastMessageAt: tagged.Add(2 * time.Hour)},
	}}
	kick := &fakeKick{}
	prev := &fakePrev{preview: &cleanup.Preview{}}
	svc := newSvc(t, prev, kick, mem, &fakeOut{}, store, gracekick.Config{Batch: 15})

	sum, err := svc.RunDaily(context.Background(), absChat, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(kick.kicked) != 0 {
		t.Fatalf("a member who wrote after the tag must NOT be kicked, kicked=%v", kick.kicked)
	}
	if sum.Saved != 1 {
		t.Fatalf("want Saved=1, got %d", sum.Saved)
	}
	if _, ok := store.recs[1]; ok {
		t.Fatal("a saved member's ticket must be cleared")
	}
}

func TestSweepSavesOnReactionAfterTag(t *testing.T) {
	now := time.Now().UTC()
	tagged := now.Add(-96 * time.Hour)
	store := newMemStore()
	openTicket(store, 7, tagged, now.Add(-1*time.Hour))
	mem := &fakeMembers{m: map[int64]*membership.Member{
		7: {UserID: 7, AbsChatID: absChat, LastReactionAt: tagged.Add(30 * time.Minute)},
	}}
	kick := &fakeKick{}
	svc := newSvc(t, &fakePrev{preview: &cleanup.Preview{}}, kick, mem, &fakeOut{}, store, gracekick.Config{Batch: 15})

	sum, err := svc.RunDaily(context.Background(), absChat, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(kick.kicked) != 0 || sum.Saved != 1 {
		t.Fatalf("a reaction after the tag must spare the member: kicked=%v saved=%d", kick.kicked, sum.Saved)
	}
}

func TestSweepKicksStillSilentPastDeadline(t *testing.T) {
	now := time.Now().UTC()
	tagged := now.Add(-96 * time.Hour)
	store := newMemStore()
	openTicket(store, 1, tagged, now.Add(-24*time.Hour))
	// activity is all BEFORE the tag -> still silent since tagging.
	mem := &fakeMembers{m: map[int64]*membership.Member{
		1: {UserID: 1, AbsChatID: absChat, LastMessageAt: tagged.Add(-200 * time.Hour)},
	}}
	kick := &fakeKick{}
	svc := newSvc(t, &fakePrev{preview: &cleanup.Preview{}}, kick, mem, &fakeOut{}, store, gracekick.Config{Batch: 15})

	sum, err := svc.RunDaily(context.Background(), absChat, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(kick.kicked) != 1 || kick.kicked[0] != 1 {
		t.Fatalf("a still-silent member past deadline must be kicked, kicked=%v", kick.kicked)
	}
	if sum.Kicked != 1 {
		t.Fatalf("want Kicked=1, got %d", sum.Kicked)
	}
	if _, ok := store.recs[1]; ok {
		t.Fatal("a kicked member's ticket must be cleared")
	}
}

func TestSweepLeavesUnexpiredTicketUntouchedAndPreventsRetag(t *testing.T) {
	now := time.Now().UTC()
	store := newMemStore()
	openTicket(store, 1, now.Add(-1*time.Hour), now.Add(71*time.Hour)) // still in grace
	kick := &fakeKick{}
	// user 1 is also "stale" in the preview - must NOT be re-tagged while
	// an unexpired ticket exists.
	prev := &fakePrev{preview: &cleanup.Preview{Candidates: []membership.Member{member(1)}}}
	out := &fakeOut{}
	svc := newSvc(t, prev, kick, &fakeMembers{}, out, store, gracekick.Config{Grace: 72 * time.Hour, Batch: 15})

	sum, err := svc.RunDaily(context.Background(), absChat, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(kick.kicked) != 0 {
		t.Fatal("an unexpired ticket must not trigger a kick")
	}
	if sum.Tagged != 0 || len(out.sent) != 0 {
		t.Fatal("a member with an open unexpired ticket must not be re-tagged")
	}
	if _, ok := store.recs[1]; !ok {
		t.Fatal("the unexpired ticket must survive the run")
	}
}

// ---- critic regression coverage ---------------------------------------

// B1: a proven-stale member whose live identity lookup FAILED
// (Resolved=false, Present=false) must never be publicly tagged or
// ticketed - it is the "no evidence to act publicly" case.
func TestRunDailyNeverTagsUnresolvedMember(t *testing.T) {
	now := time.Now().UTC()
	prev := &fakePrev{
		preview: &cleanup.Preview{Candidates: []membership.Member{member(1), member(2)}},
		resolved: map[int64]cleanup.ResolvedMember{
			1: {Resolved: false, Present: false}, // getChatMember failed
			2: {Resolved: false, Present: true},  // present but unnameable
		},
	}
	store := newMemStore()
	out := &fakeOut{}
	svc := newSvc(t, prev, &fakeKick{}, &fakeMembers{}, out, store, gracekick.Config{Batch: 15})

	sum, err := svc.RunDaily(context.Background(), absChat, now)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Tagged != 0 || len(store.recs) != 0 || len(out.sent) != 0 {
		t.Fatalf("an unidentified member must never be tagged: tagged=%d recs=%d sent=%d",
			sum.Tagged, len(store.recs), len(out.sent))
	}
}

// S1: a store read error while checking reappearance must NOT translate
// into a kick - never remove on uncertainty; keep the ticket.
func TestSweepDoesNotKickWhenReappearanceUndeterminable(t *testing.T) {
	now := time.Now().UTC()
	store := newMemStore()
	openTicket(store, 1, now.Add(-96*time.Hour), now.Add(-24*time.Hour)) // past deadline
	kick := &fakeKick{}
	mem := &fakeMembers{err: errors.New("bbolt read failed")}
	svc := newSvc(t, &fakePrev{preview: &cleanup.Preview{}}, kick, mem, &fakeOut{}, store, gracekick.Config{Batch: 15})

	sum, err := svc.RunDaily(context.Background(), absChat, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(kick.kicked) != 0 || sum.Kicked != 0 {
		t.Fatalf("an undeterminable member must not be kicked, kicked=%v", kick.kicked)
	}
	if _, ok := store.recs[1]; !ok {
		t.Fatal("the ticket must be retained for re-evaluation, not deleted")
	}
}

// S1b: a missing live record (ErrNotFound) is likewise undeterminable -
// spare, do not kick.
func TestSweepDoesNotKickWhenNoLiveRecord(t *testing.T) {
	now := time.Now().UTC()
	store := newMemStore()
	openTicket(store, 1, now.Add(-96*time.Hour), now.Add(-24*time.Hour))
	kick := &fakeKick{}
	svc := newSvc(t, &fakePrev{preview: &cleanup.Preview{}}, kick,
		&fakeMembers{}, &fakeOut{}, store, gracekick.Config{Batch: 15})

	sum, err := svc.RunDaily(context.Background(), absChat, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(kick.kicked) != 0 || sum.Kicked != 0 {
		t.Fatalf("missing live record must not cause a kick, kicked=%v", kick.kicked)
	}
	if _, ok := store.recs[1]; !ok {
		t.Fatal("ticket must be retained when reappearance cannot be determined")
	}
}

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

// S3: an oversized batch is trimmed to one message that fits Telegram's
// 4096 UTF-16-unit limit; tickets persist ONLY for members actually
// sent, so the lifecycle can never 400-loop and stall the chat. Names
// here are ASTRAL emoji (1 rune = 2 UTF-16 units) - the exact adversarial
// case a rune-based budget undercounts.
func TestRunDailyTrimsOversizedAnnouncementToOneMessage(t *testing.T) {
	now := time.Now().UTC()
	astralName := strings.Repeat("😀", 32) // 32 runes = 64 UTF-16 units
	var cands []membership.Member
	resolved := map[int64]cleanup.ResolvedMember{}
	for i := int64(1); i <= 50; i++ {
		m := membership.Member{UserID: i, AbsChatID: absChat, Status: membership.StatusMember, FirstName: astralName}
		cands = append(cands, m)
		resolved[i] = cleanup.ResolvedMember{Resolved: true, Present: true}
	}
	prev := &fakePrev{preview: &cleanup.Preview{Candidates: cands}, resolved: resolved}
	store := newMemStore()
	out := &fakeOut{}
	svc := newSvc(t, prev, &fakeKick{}, &fakeMembers{}, out, store, gracekick.Config{Batch: 50})

	sum, err := svc.RunDaily(context.Background(), absChat, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.sent) != 1 {
		t.Fatalf("exactly one public message expected, got %d", len(out.sent))
	}
	if u := utf16Units(out.sent[0]); u >= 4096 {
		t.Fatalf("announcement must stay under Telegram's 4096 UTF-16 limit, got %d units", u)
	}
	if sum.Tagged == 0 || sum.Tagged >= 50 {
		t.Fatalf("oversized batch must be trimmed (0 < tagged < 50), got %d", sum.Tagged)
	}
	if len(store.recs) != sum.Tagged {
		t.Fatalf("tickets must exist only for members actually announced: recs=%d tagged=%d",
			len(store.recs), sum.Tagged)
	}
}
