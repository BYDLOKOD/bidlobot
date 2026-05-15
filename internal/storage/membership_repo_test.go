package storage_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/storage"
)

func newMembershipRepo(t *testing.T) *storage.MembershipRepo {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := storage.NewBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return storage.NewMembershipRepo(s.DB())
}

func strPtr(s string) *string { return &s }

func TestMemberUpsertCreatesNew(t *testing.T) {
	repo := newMembershipRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()

	m, err := repo.UpsertMember(ctx, membership.MemberPatch{
		UserID:          111,
		AbsChatID:       100,
		Username:        strPtr("Alice"),
		FirstName:       strPtr("Alice"),
		Status:          membership.StatusMember,
		KnownVia:        membership.SourceMessage,
		LastMessageAt:   now,
		IncMessageCount: 1,
		Now:             now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.MessageCount != 1 {
		t.Fatalf("expected count 1, got %d", m.MessageCount)
	}
	if m.Username != "alice" {
		t.Fatalf("username should be lowercased, got %q", m.Username)
	}
	if !m.FirstSeenAt.Equal(now) {
		t.Fatalf("FirstSeenAt should equal now, got %v", m.FirstSeenAt)
	}
	if m.Status != membership.StatusMember {
		t.Fatalf("expected status member, got %s", m.Status)
	}
}

func TestMemberUpsertMergesPatches(t *testing.T) {
	repo := newMembershipRepo(t)
	ctx := context.Background()
	t1 := time.Now().UTC()
	t2 := t1.Add(time.Minute)
	t3 := t2.Add(time.Hour)

	_, _ = repo.UpsertMember(ctx, membership.MemberPatch{
		UserID:          111,
		AbsChatID:       100,
		Username:        strPtr("alice"),
		Status:          membership.StatusMember,
		KnownVia:        membership.SourceMessage,
		LastMessageAt:   t1,
		IncMessageCount: 5,
		Now:             t1,
	})
	_, _ = repo.UpsertMember(ctx, membership.MemberPatch{
		UserID:           111,
		AbsChatID:        100,
		LastReactionAt:   t2,
		IncReactionCount: 3,
		KnownVia:         membership.SourceReaction,
		Now:              t2,
	})
	m, _ := repo.UpsertMember(ctx, membership.MemberPatch{
		UserID:          111,
		AbsChatID:       100,
		LastMessageAt:   t3,
		IncMessageCount: 2,
		Now:             t3,
	})
	if m.MessageCount != 7 {
		t.Fatalf("expected message count 7, got %d", m.MessageCount)
	}
	if m.ReactionCount != 3 {
		t.Fatalf("expected reaction count 3, got %d", m.ReactionCount)
	}
	if !m.LastMessageAt.Equal(t3) {
		t.Fatalf("LastMessageAt should be t3, got %v", m.LastMessageAt)
	}
	if !m.LastReactionAt.Equal(t2) {
		t.Fatalf("LastReactionAt should be t2, got %v", m.LastReactionAt)
	}
	if !m.LastSeenAt.Equal(t3) {
		t.Fatalf("LastSeenAt should be t3, got %v", m.LastSeenAt)
	}
	if m.Username != "alice" {
		t.Fatalf("username preserved: got %q", m.Username)
	}
}

func TestMemberUpsertTimestampsOnlyForward(t *testing.T) {
	repo := newMembershipRepo(t)
	ctx := context.Background()
	late := time.Now().UTC()
	early := late.Add(-time.Hour)

	_, _ = repo.UpsertMember(ctx, membership.MemberPatch{
		UserID: 111, AbsChatID: 100, LastMessageAt: late, Now: late,
	})
	m, _ := repo.UpsertMember(ctx, membership.MemberPatch{
		UserID: 111, AbsChatID: 100, LastMessageAt: early, Now: early,
	})
	if !m.LastMessageAt.Equal(late) {
		t.Fatalf("LastMessageAt must not move backwards: got %v want %v", m.LastMessageAt, late)
	}
	if !m.LastSeenAt.Equal(late) {
		t.Fatalf("LastSeenAt must not move backwards: got %v want %v", m.LastSeenAt, late)
	}
}

func TestMemberUpsertEmptyPatchValid(t *testing.T) {
	repo := newMembershipRepo(t)
	ctx := context.Background()
	_, err := repo.UpsertMember(ctx, membership.MemberPatch{
		UserID: 111, AbsChatID: 100, Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMemberUpsertRejectsZeroIDs(t *testing.T) {
	repo := newMembershipRepo(t)
	ctx := context.Background()
	if _, err := repo.UpsertMember(ctx, membership.MemberPatch{UserID: 0, AbsChatID: 100}); err == nil {
		t.Fatal("expected error for zero UserID")
	}
	if _, err := repo.UpsertMember(ctx, membership.MemberPatch{UserID: 1, AbsChatID: 0}); err == nil {
		t.Fatal("expected error for zero AbsChatID")
	}
}

func TestMemberGetByUsernameCaseInsensitive(t *testing.T) {
	repo := newMembershipRepo(t)
	ctx := context.Background()
	_, _ = repo.UpsertMember(ctx, membership.MemberPatch{
		UserID: 111, AbsChatID: 100, Username: strPtr("  AliceCool  "), Now: time.Now(),
	})

	for _, q := range []string{"alicecool", "AliceCool", "ALICECOOL", "  alicecool "} {
		m, err := repo.GetMemberByUsername(ctx, 100, q)
		if err != nil {
			t.Fatalf("query %q: %v", q, err)
		}
		if m.UserID != 111 {
			t.Fatalf("query %q: expected user 111, got %d", q, m.UserID)
		}
	}

	if _, err := repo.GetMemberByUsername(ctx, 100, "nobody"); err != membership.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if _, err := repo.GetMemberByUsername(ctx, 100, ""); err != membership.ErrNotFound {
		t.Fatalf("empty username should be ErrNotFound, got %v", err)
	}
}

func TestMemberListByChatFiltersByChat(t *testing.T) {
	repo := newMembershipRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for _, uid := range []int64{111, 222, 333} {
		_, _ = repo.UpsertMember(ctx, membership.MemberPatch{
			UserID: uid, AbsChatID: 100, IncMessageCount: 1, Now: now,
		})
	}
	_, _ = repo.UpsertMember(ctx, membership.MemberPatch{
		UserID: 111, AbsChatID: 999, IncMessageCount: 1, Now: now,
	})

	list, err := repo.ListByChat(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 members in chat 100, got %d", len(list))
	}

	list2, _ := repo.ListByChat(ctx, 999)
	if len(list2) != 1 {
		t.Fatalf("expected 1 member in chat 999, got %d", len(list2))
	}
}

func TestMemberConcurrentUpserts(t *testing.T) {
	repo := newMembershipRepo(t)
	ctx := context.Background()
	const goroutines = 50
	const perGoroutine = 10

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				_, _ = repo.UpsertMember(ctx, membership.MemberPatch{
					UserID: 111, AbsChatID: 100, IncMessageCount: 1, Now: time.Now(),
				})
			}
		}()
	}
	wg.Wait()

	m, err := repo.GetMember(ctx, 111, 100)
	if err != nil {
		t.Fatal(err)
	}
	if m.MessageCount != int64(goroutines*perGoroutine) {
		t.Fatalf("expected count %d, got %d", goroutines*perGoroutine, m.MessageCount)
	}
}

func TestChatUpsertPreservesInstalledAt(t *testing.T) {
	repo := newMembershipRepo(t)
	ctx := context.Background()
	t1 := time.Now().UTC()
	t2 := t1.Add(time.Hour)

	_ = repo.UpsertChat(ctx, membership.Chat{
		AbsChatID: 100, Title: "Original", BotStatus: membership.StatusAdministrator,
		InstalledAt: t1, LastUpdateAt: t1,
	})
	_ = repo.UpsertChat(ctx, membership.Chat{
		AbsChatID: 100, Title: "Renamed", BotStatus: membership.StatusAdministrator,
		LastUpdateAt: t2,
	})

	c, err := repo.GetChat(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if c.Title != "Renamed" {
		t.Fatalf("title should update, got %q", c.Title)
	}
	if !c.InstalledAt.Equal(t1) {
		t.Fatalf("InstalledAt should preserve original, got %v", c.InstalledAt)
	}
	if !c.LastUpdateAt.Equal(t2) {
		t.Fatalf("LastUpdateAt should refresh, got %v", c.LastUpdateAt)
	}
}

func TestChatGetNotFound(t *testing.T) {
	repo := newMembershipRepo(t)
	ctx := context.Background()
	if _, err := repo.GetChat(ctx, 999); err != membership.ErrChatNotFound {
		t.Fatalf("expected ErrChatNotFound, got %v", err)
	}
}

func TestChatListAll(t *testing.T) {
	repo := newMembershipRepo(t)
	ctx := context.Background()
	for _, id := range []int64{100, 200, 300} {
		_ = repo.UpsertChat(ctx, membership.Chat{AbsChatID: id, BotStatus: membership.StatusAdministrator})
	}
	list, err := repo.ListChats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 chats, got %d", len(list))
	}
}

func i64(v int64) *int64 { return &v }

// TestMemberSetCountMaxSemantics locks the import contract: SetMessageCount
// is applied as max(existing, value) so (a) re-running the same import is
// idempotent and (b) a realtime count accumulated since deploy is never
// reduced by a stale import snapshot.
func TestMemberSetCountMaxSemantics(t *testing.T) {
	repo := newMembershipRepo(t)
	ctx := context.Background()
	hist := time.Date(2025, 8, 5, 0, 0, 0, 0, time.UTC)

	// First import: 1000 historical messages.
	m, err := repo.UpsertMember(ctx, membership.MemberPatch{
		UserID: 42, AbsChatID: 100,
		FirstName:       strPtr("Олег"),
		Status:          membership.StatusMember,
		KnownVia:        membership.SourceImport,
		LastMessageAt:   hist,
		SetMessageCount: i64(1000),
		Now:             hist,
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.MessageCount != 1000 {
		t.Fatalf("first import: want 1000, got %d", m.MessageCount)
	}
	if m.LastSeenAt != hist {
		t.Fatalf("LastSeenAt should be the historical date for preview sort, got %v", m.LastSeenAt)
	}

	// Realtime activity after import adds 5 live messages.
	for i := 0; i < 5; i++ {
		if _, err := repo.UpsertMember(ctx, membership.MemberPatch{
			UserID: 42, AbsChatID: 100,
			KnownVia:        membership.SourceMessage,
			LastMessageAt:   time.Now().UTC(),
			IncMessageCount: 1,
			Now:             time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	mid, _ := repo.GetMember(ctx, 42, 100)
	if mid.MessageCount != 1005 {
		t.Fatalf("after realtime: want 1005, got %d", mid.MessageCount)
	}

	// Re-running the SAME import must NOT clobber the realtime delta:
	// max(1005, 1000) = 1005. Idempotent and non-destructive.
	m2, err := repo.UpsertMember(ctx, membership.MemberPatch{
		UserID: 42, AbsChatID: 100,
		KnownVia:        membership.SourceImport,
		SetMessageCount: i64(1000),
		Now:             hist,
	})
	if err != nil {
		t.Fatal(err)
	}
	if m2.MessageCount != 1005 {
		t.Fatalf("re-import must not reduce count: want 1005, got %d", m2.MessageCount)
	}
	if m2.KnownVia != membership.SourceImport {
		t.Fatalf("KnownVia should reflect last writer, got %s", m2.KnownVia)
	}
}
