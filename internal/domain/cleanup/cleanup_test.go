package cleanup_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/domain/cleanup"
	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/testutil"
)

// inMemMembership is a tight Store stub that fits in one test file. Real
// store behaviour is covered by storage/membership_repo_test.go.
type inMemMembership struct {
	mu      sync.Mutex
	members map[int64]map[int64]membership.Member // absChatID -> userID -> Member
	chats   map[int64]membership.Chat
	listErr error
}

func newInMem() *inMemMembership {
	return &inMemMembership{
		members: make(map[int64]map[int64]membership.Member),
		chats:   make(map[int64]membership.Chat),
	}
}

func (s *inMemMembership) UpsertMember(_ context.Context, p membership.MemberPatch) (*membership.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	chat, ok := s.members[p.AbsChatID]
	if !ok {
		chat = make(map[int64]membership.Member)
		s.members[p.AbsChatID] = chat
	}
	m := chat[p.UserID]
	m.UserID = p.UserID
	m.AbsChatID = p.AbsChatID
	chat[p.UserID] = m
	return &m, nil
}

func (s *inMemMembership) GetMember(_ context.Context, userID, absChatID int64) (*membership.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	chat, ok := s.members[absChatID]
	if !ok {
		return nil, membership.ErrNotFound
	}
	m, ok := chat[userID]
	if !ok {
		return nil, membership.ErrNotFound
	}
	return &m, nil
}

func (s *inMemMembership) GetMemberByUsername(_ context.Context, _ int64, _ string) (*membership.Member, error) {
	return nil, membership.ErrNotFound
}

func (s *inMemMembership) ListByChat(_ context.Context, absChatID int64) ([]membership.Member, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	chat, ok := s.members[absChatID]
	if !ok {
		return nil, nil
	}
	out := make([]membership.Member, 0, len(chat))
	for _, m := range chat {
		out = append(out, m)
	}
	return out, nil
}

func (s *inMemMembership) UpsertChat(_ context.Context, c membership.Chat) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chats[c.AbsChatID] = c
	return nil
}

func (s *inMemMembership) GetChat(_ context.Context, absChatID int64) (*membership.Chat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.chats[absChatID]
	if !ok {
		return nil, membership.ErrChatNotFound
	}
	return &c, nil
}

func (s *inMemMembership) ListChats(_ context.Context) ([]membership.Chat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]membership.Chat, 0, len(s.chats))
	for _, c := range s.chats {
		out = append(out, c)
	}
	return out, nil
}

func (s *inMemMembership) put(m membership.Member) {
	s.mu.Lock()
	defer s.mu.Unlock()
	chat, ok := s.members[m.AbsChatID]
	if !ok {
		chat = make(map[int64]membership.Member)
		s.members[m.AbsChatID] = chat
	}
	chat[m.UserID] = m
}

func newSvc(t *testing.T, api *testutil.MockAPI) (*cleanup.Service, *inMemMembership) {
	t.Helper()
	store := newInMem()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	svc := cleanup.NewService(store, api, log)
	svc.SetKickInterval(time.Millisecond) // fast tests
	return svc, store
}

func TestPreviewRejectsTinyThreshold(t *testing.T) {
	svc, _ := newSvc(t, testutil.NewMockAPI())
	_, err := svc.PreviewInactive(context.Background(), 100, time.Minute, time.Now())
	if !errors.Is(err, cleanup.ErrThresholdTooSmall) {
		t.Fatalf("expected ErrThresholdTooSmall, got %v", err)
	}
}

func TestPreviewRejectsHugeThreshold(t *testing.T) {
	svc, _ := newSvc(t, testutil.NewMockAPI())
	_, err := svc.PreviewInactive(context.Background(), 100, 100*365*24*time.Hour, time.Now())
	if !errors.Is(err, cleanup.ErrThresholdTooLarge) {
		t.Fatalf("expected ErrThresholdTooLarge, got %v", err)
	}
}

func TestPreviewIncludesNeverActiveMembers(t *testing.T) {
	svc, store := newSvc(t, testutil.NewMockAPI())
	store.put(membership.Member{UserID: 111, AbsChatID: 100, Status: membership.StatusMember})
	store.put(membership.Member{UserID: 222, AbsChatID: 100, Status: membership.StatusMember})

	preview, err := svc.PreviewInactive(context.Background(), 100, 30*24*time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(preview.Candidates))
	}
	if preview.KnownMembers != 2 {
		t.Fatalf("KnownMembers should be 2, got %d", preview.KnownMembers)
	}
}

func TestPreviewExcludesActiveMembers(t *testing.T) {
	svc, store := newSvc(t, testutil.NewMockAPI())
	now := time.Now().UTC()
	store.put(membership.Member{UserID: 111, AbsChatID: 100, Status: membership.StatusMember, LastMessageAt: now.Add(-5 * 24 * time.Hour)})
	store.put(membership.Member{UserID: 222, AbsChatID: 100, Status: membership.StatusMember, LastReactionAt: now.Add(-5 * 24 * time.Hour)})

	preview, err := svc.PreviewInactive(context.Background(), 100, 30*24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Candidates) != 0 {
		t.Fatalf("active members must not be candidates, got %d", len(preview.Candidates))
	}
}

func TestPreviewExcludesAdminsAndBotsAndKicked(t *testing.T) {
	svc, store := newSvc(t, testutil.NewMockAPI())
	store.put(membership.Member{UserID: 1, AbsChatID: 100, Status: membership.StatusAdministrator})
	store.put(membership.Member{UserID: 2, AbsChatID: 100, Status: membership.StatusCreator})
	store.put(membership.Member{UserID: 3, AbsChatID: 100, Status: membership.StatusMember, IsBot: true})
	store.put(membership.Member{UserID: 4, AbsChatID: 100, Status: membership.StatusKicked})
	store.put(membership.Member{UserID: 5, AbsChatID: 100, Status: membership.StatusLeft})
	store.put(membership.Member{UserID: 6, AbsChatID: 100, Status: membership.StatusMember}) // the only candidate

	preview, err := svc.PreviewInactive(context.Background(), 100, 30*24*time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Candidates) != 1 || preview.Candidates[0].UserID != 6 {
		t.Fatalf("expected only user 6, got %+v", preview.Candidates)
	}
}

func TestPreviewExcludesAnonymousAdminID(t *testing.T) {
	svc, store := newSvc(t, testutil.NewMockAPI())
	store.put(membership.Member{UserID: 1087968824, AbsChatID: 100, Status: membership.StatusMember})
	preview, err := svc.PreviewInactive(context.Background(), 100, 30*24*time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Candidates) != 0 {
		t.Fatalf("anonymous admin must never be a candidate")
	}
}

func TestPreviewSortsLeastRecentlySeenFirst(t *testing.T) {
	svc, store := newSvc(t, testutil.NewMockAPI())
	now := time.Now().UTC()
	store.put(membership.Member{UserID: 1, AbsChatID: 100, Status: membership.StatusMember, LastSeenAt: now.Add(-100 * 24 * time.Hour)})
	store.put(membership.Member{UserID: 2, AbsChatID: 100, Status: membership.StatusMember, LastSeenAt: now.Add(-300 * 24 * time.Hour)})
	store.put(membership.Member{UserID: 3, AbsChatID: 100, Status: membership.StatusMember, LastSeenAt: now.Add(-200 * 24 * time.Hour)})

	preview, err := svc.PreviewInactive(context.Background(), 100, 30*24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Candidates) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(preview.Candidates))
	}
	wantOrder := []int64{2, 3, 1}
	for i, want := range wantOrder {
		if preview.Candidates[i].UserID != want {
			t.Fatalf("position %d: want user %d, got %d", i, want, preview.Candidates[i].UserID)
		}
	}
}

func TestPreviewObservationWindowSetWhenChatRegistered(t *testing.T) {
	svc, store := newSvc(t, testutil.NewMockAPI())
	now := time.Now().UTC()
	installed := now.Add(-30 * 24 * time.Hour)
	_ = store.UpsertChat(context.Background(), membership.Chat{
		AbsChatID: 100, InstalledAt: installed, BotStatus: membership.StatusAdministrator,
	})

	preview, err := svc.PreviewInactive(context.Background(), 100, 24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.InstalledAt.Equal(installed) {
		t.Fatalf("InstalledAt should propagate, got %v", preview.InstalledAt)
	}
	want := now.Sub(installed)
	if preview.ObservationWindow != want {
		t.Fatalf("ObservationWindow %v, want %v", preview.ObservationWindow, want)
	}
}

func TestPreviewObservationWindowZeroWithoutChat(t *testing.T) {
	svc, _ := newSvc(t, testutil.NewMockAPI())
	preview, err := svc.PreviewInactive(context.Background(), 100, 24*time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !preview.InstalledAt.IsZero() {
		t.Fatalf("InstalledAt should be zero when chat unknown, got %v", preview.InstalledAt)
	}
	if preview.ObservationWindow != 0 {
		t.Fatalf("ObservationWindow should be 0 when chat unknown, got %v", preview.ObservationWindow)
	}
}

func TestExecuteCleanupKicksAllCandidates(t *testing.T) {
	api := testutil.NewMockAPI()
	svc, _ := newSvc(t, api)

	candidates := []membership.Member{
		{UserID: 1, AbsChatID: 100, Status: membership.StatusMember, Username: "alice"},
		{UserID: 2, AbsChatID: 100, Status: membership.StatusMember, Username: "bob"},
	}

	report, err := svc.ExecuteCleanup(context.Background(), -100, candidates, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Total != 2 || report.Kicked != 2 {
		t.Fatalf("expected 2 kicked, got Kicked=%d Total=%d", report.Kicked, report.Total)
	}
	if api.CallCount("BanChatMember") != 2 {
		t.Fatalf("expected 2 BanChatMember calls, got %d", api.CallCount("BanChatMember"))
	}
	if api.CallCount("UnbanChatMember") != 2 {
		t.Fatalf("expected 2 UnbanChatMember calls, got %d", api.CallCount("UnbanChatMember"))
	}
}

func TestExecuteCleanupSkipsAdminPromotion(t *testing.T) {
	api := testutil.NewMockAPI()
	api.ChatMembers["100:1"] = "administrator"
	svc, _ := newSvc(t, api)

	report, err := svc.ExecuteCleanup(context.Background(), -100, []membership.Member{
		{UserID: 1, AbsChatID: 100, Status: membership.StatusMember},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Skipped != 1 || report.Kicked != 0 {
		t.Fatalf("expected skip-admin, got %+v", report)
	}
	if api.CallCount("BanChatMember") != 0 {
		t.Fatal("must not call BanChatMember on admin")
	}
}

func TestExecuteCleanupSkipsAlreadyLeft(t *testing.T) {
	api := testutil.NewMockAPI()
	api.ChatMembers["100:1"] = "left"
	svc, _ := newSvc(t, api)

	report, err := svc.ExecuteCleanup(context.Background(), -100, []membership.Member{
		{UserID: 1, AbsChatID: 100, Status: membership.StatusMember},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Skipped != 1 || report.Kicked != 0 {
		t.Fatalf("expected skip-already, got %+v", report)
	}
}

func TestExecuteCleanupReportsFailures(t *testing.T) {
	api := &fakeBanFailureAPI{MockAPI: testutil.NewMockAPI(), banErr: errors.New("not enough rights")}
	store := newInMem()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	svc := cleanup.NewService(store, api, log)
	svc.SetKickInterval(time.Millisecond)

	report, err := svc.ExecuteCleanup(context.Background(), -100, []membership.Member{
		{UserID: 1, AbsChatID: 100, Status: membership.StatusMember},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Failed != 1 {
		t.Fatalf("expected 1 failed, got %+v", report)
	}
	if len(report.Entries) != 1 || report.Entries[0].APIError == "" {
		t.Fatal("entry must carry the API error")
	}
}

func TestExecuteCleanupRespectsContextCancellation(t *testing.T) {
	svc, _ := newSvc(t, testutil.NewMockAPI())
	svc.SetKickInterval(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	candidates := make([]membership.Member, 10)
	for i := range candidates {
		candidates[i] = membership.Member{UserID: int64(i + 1), AbsChatID: 100, Status: membership.StatusMember}
	}

	report, err := svc.ExecuteCleanup(ctx, -100, candidates, nil)
	if err == nil {
		t.Fatal("expected context error")
	}
	if report.Total != 10 || report.Kicked == 10 {
		t.Fatalf("should have stopped early, got %+v", report)
	}
}

func TestExecuteCleanupProgressCallback(t *testing.T) {
	svc, _ := newSvc(t, testutil.NewMockAPI())
	candidates := []membership.Member{
		{UserID: 1, AbsChatID: 100, Status: membership.StatusMember},
		{UserID: 2, AbsChatID: 100, Status: membership.StatusMember},
	}
	var calls []int
	_, err := svc.ExecuteCleanup(context.Background(), -100, candidates, func(done, total int, _ cleanup.ExecutionEntry) {
		calls = append(calls, done)
		if total != 2 {
			t.Errorf("total should always be 2, got %d", total)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != 1 || calls[1] != 2 {
		t.Fatalf("unexpected progress sequence: %v", calls)
	}
}

// fakeBanFailureAPI lets us steer banChatMember to fail without
// touching any other behaviour of the standard mock.
type fakeBanFailureAPI struct {
	*testutil.MockAPI
	banErr error
}

func (f *fakeBanFailureAPI) BanChatMember(_ context.Context, _ *telego.BanChatMemberParams) error {
	return f.banErr
}

func (f *fakeBanFailureAPI) String() string { return fmt.Sprintf("fake api err=%v", f.banErr) }
