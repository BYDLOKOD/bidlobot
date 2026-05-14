package storage_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/domain/moderation"
	"github.com/veschin/bidlobot/internal/domain/stats"
	"github.com/veschin/bidlobot/internal/storage"
)

func newMigrationStore(t *testing.T) *storage.BoltStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "migrate.db")
	s, err := storage.NewBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrateChatID_RekeysAllBuckets(t *testing.T) {
	store := newMigrationStore(t)
	ctx := context.Background()
	const oldAbs int64 = 1234567890
	const newAbs int64 = 9876543210

	// Seed stats: two users.
	statsRepo := storage.NewStatsRepo(store.DB())
	now := time.Now().UTC()
	if err := statsRepo.Flush(ctx, map[stats.FlushKey]*stats.FlushDelta{
		{UserID: 100, AbsChatID: oldAbs}: {CountDelta: 5, FirstSeen: now, LastSeen: now},
		{UserID: 200, AbsChatID: oldAbs}: {CountDelta: 3, FirstSeen: now, LastSeen: now},
		// Unrelated chat to confirm we don't touch it.
		{UserID: 100, AbsChatID: 555}: {CountDelta: 1, FirstSeen: now, LastSeen: now},
	}); err != nil {
		t.Fatalf("seed stats: %v", err)
	}

	// Seed members.
	memberRepo := storage.NewMembershipRepo(store.DB())
	username1 := "alice"
	if _, err := memberRepo.UpsertMember(ctx, membership.MemberPatch{
		UserID:    100,
		AbsChatID: oldAbs,
		Username:  &username1,
		Status:    membership.StatusMember,
		Now:       now,
	}); err != nil {
		t.Fatal(err)
	}
	username2 := "bob"
	if _, err := memberRepo.UpsertMember(ctx, membership.MemberPatch{
		UserID:    200,
		AbsChatID: oldAbs,
		Username:  &username2,
		Status:    membership.StatusMember,
		Now:       now,
	}); err != nil {
		t.Fatal(err)
	}
	// Seed unrelated chat membership.
	if _, err := memberRepo.UpsertMember(ctx, membership.MemberPatch{
		UserID:    100,
		AbsChatID: 555,
		Username:  &username1,
		Status:    membership.StatusMember,
		Now:       now,
	}); err != nil {
		t.Fatal(err)
	}

	// Seed chats.
	if err := memberRepo.UpsertChat(ctx, membership.Chat{
		AbsChatID:    oldAbs,
		Title:        "Old Group",
		Type:         "group",
		BotStatus:    membership.StatusAdministrator,
		LastUpdateAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	// Seed warnings: 3 for user 200 in old chat.
	warnRepo := storage.NewWarnRepo(store.DB())
	for i := 0; i < 3; i++ {
		if _, err := warnRepo.CreateWarning(ctx, &moderation.Warning{
			ID:           uuid.NewString(),
			TargetUserID: 200,
			ChatID:       oldAbs,
			IssuerUserID: 100,
			Reason:       "spam",
			Timestamp:    now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Unrelated warning in another chat.
	unrelatedID := uuid.NewString()
	if _, err := warnRepo.CreateWarning(ctx, &moderation.Warning{
		ID:           unrelatedID,
		TargetUserID: 200,
		ChatID:       555,
		IssuerUserID: 100,
		Timestamp:    now,
	}); err != nil {
		t.Fatal(err)
	}

	// Migrate.
	report, err := storage.MigrateChatID(ctx, store.DB(), oldAbs, newAbs)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if report.StatsRekeyed != 2 || report.StatsIndexes != 2 {
		t.Errorf("stats counters: %+v", report)
	}
	if report.Members != 2 || report.MemberIndex != 2 {
		t.Errorf("member counters: %+v", report)
	}
	if report.Chats != 1 {
		t.Errorf("chats counter: %+v", report)
	}
	if report.Warnings != 3 || report.WarnIndexes != 3 {
		t.Errorf("warning counters: %+v", report)
	}

	// Verify stats moved.
	if _, err := statsRepo.Get(ctx, 100, oldAbs); err == nil {
		t.Errorf("stats user 100 still readable at oldAbs")
	}
	got, err := statsRepo.Get(ctx, 100, newAbs)
	if err != nil {
		t.Fatalf("stats user 100 missing at newAbs: %v", err)
	}
	if got.ChatID != newAbs {
		t.Errorf("stats.ChatID expected %d, got %d", newAbs, got.ChatID)
	}
	if got.MessageCount != 5 {
		t.Errorf("stats lost message count: %d", got.MessageCount)
	}

	listOld, _ := statsRepo.ListByChat(ctx, oldAbs)
	if len(listOld) != 0 {
		t.Errorf("oldAbs should have 0 stats, got %d", len(listOld))
	}
	listNew, _ := statsRepo.ListByChat(ctx, newAbs)
	if len(listNew) != 2 {
		t.Errorf("newAbs should have 2 stats, got %d", len(listNew))
	}

	// Unrelated chat should be untouched.
	listOther, _ := statsRepo.ListByChat(ctx, 555)
	if len(listOther) != 1 {
		t.Errorf("unrelated chat should have 1 stats, got %d", len(listOther))
	}

	// Verify members moved.
	if _, err := memberRepo.GetMember(ctx, 100, oldAbs); err == nil {
		t.Errorf("member 100 still at oldAbs")
	}
	mem, err := memberRepo.GetMember(ctx, 100, newAbs)
	if err != nil {
		t.Fatalf("member 100 missing at newAbs: %v", err)
	}
	if mem.AbsChatID != newAbs {
		t.Errorf("member.AbsChatID expected %d, got %d", newAbs, mem.AbsChatID)
	}
	if mem.Username != "alice" {
		t.Errorf("member alice lost: %v", mem)
	}

	memListOld, _ := memberRepo.ListByChat(ctx, oldAbs)
	if len(memListOld) != 0 {
		t.Errorf("oldAbs members should be 0, got %d", len(memListOld))
	}
	memListNew, _ := memberRepo.ListByChat(ctx, newAbs)
	if len(memListNew) != 2 {
		t.Errorf("newAbs members should be 2, got %d", len(memListNew))
	}
	memListOther, _ := memberRepo.ListByChat(ctx, 555)
	if len(memListOther) != 1 {
		t.Errorf("unrelated members should be 1, got %d", len(memListOther))
	}

	// Verify by-username lookup still works after migration.
	bob, err := memberRepo.GetMemberByUsername(ctx, newAbs, "bob")
	if err != nil {
		t.Fatalf("by-username lookup post-migration: %v", err)
	}
	if bob.UserID != 200 {
		t.Errorf("by-username got wrong user: %v", bob)
	}

	// Verify chats moved.
	if _, err := memberRepo.GetChat(ctx, oldAbs); err == nil {
		t.Errorf("chat record still at oldAbs")
	}
	ch, err := memberRepo.GetChat(ctx, newAbs)
	if err != nil {
		t.Fatalf("chat record missing at newAbs: %v", err)
	}
	if ch.AbsChatID != newAbs || ch.Title != "Old Group" {
		t.Errorf("chat record corrupted: %+v", ch)
	}

	// Verify warnings moved.
	count, _ := warnRepo.CountActive(ctx, 200, oldAbs)
	if count != 0 {
		t.Errorf("oldAbs warnings should be 0, got %d", count)
	}
	count, _ = warnRepo.CountActive(ctx, 200, newAbs)
	if count != 3 {
		t.Errorf("newAbs warnings should be 3, got %d", count)
	}
	// Unrelated warning still works.
	count, _ = warnRepo.CountActive(ctx, 200, 555)
	if count != 1 {
		t.Errorf("unrelated warnings should be 1, got %d", count)
	}
	// Inspect a moved warning's value.
	moved, _ := warnRepo.ListActive(ctx, 200, newAbs)
	for _, w := range moved {
		if w.ChatID != newAbs {
			t.Errorf("warning.ChatID expected %d, got %d (id=%s)", newAbs, w.ChatID, w.ID)
		}
	}
}

func TestMigrateChatID_NoOpSameID(t *testing.T) {
	store := newMigrationStore(t)
	ctx := context.Background()
	report, err := storage.MigrateChatID(ctx, store.DB(), 42, 42)
	if err != nil {
		t.Fatal(err)
	}
	if report.StatsRekeyed+report.Members+report.Chats+report.Warnings != 0 {
		t.Errorf("expected zero work for same-id, got %+v", report)
	}
}

func TestMigrateChatID_RejectsZero(t *testing.T) {
	store := newMigrationStore(t)
	ctx := context.Background()
	if _, err := storage.MigrateChatID(ctx, store.DB(), 0, 7); err == nil {
		t.Error("expected error for zero old")
	}
	if _, err := storage.MigrateChatID(ctx, store.DB(), 7, 0); err == nil {
		t.Error("expected error for zero new")
	}
}

func TestMigrateChatID_EmptyDB(t *testing.T) {
	store := newMigrationStore(t)
	ctx := context.Background()
	report, err := storage.MigrateChatID(ctx, store.DB(), 100, 200)
	if err != nil {
		t.Fatalf("empty migrate failed: %v", err)
	}
	if report.StatsRekeyed+report.Members+report.Chats+report.Warnings != 0 {
		t.Errorf("expected zero counters on empty db: %+v", report)
	}
}

func TestMigrateChatID_PreservesChatInstalledAt(t *testing.T) {
	store := newMigrationStore(t)
	ctx := context.Background()
	memberRepo := storage.NewMembershipRepo(store.DB())

	installedAt := time.Now().Add(-30 * 24 * time.Hour).UTC()
	if err := memberRepo.UpsertChat(ctx, membership.Chat{
		AbsChatID:    111,
		InstalledAt:  installedAt,
		LastUpdateAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := storage.MigrateChatID(ctx, store.DB(), 111, 222); err != nil {
		t.Fatal(err)
	}
	got, err := memberRepo.GetChat(ctx, 222)
	if err != nil {
		t.Fatalf("get migrated chat: %v", err)
	}
	if !got.InstalledAt.Equal(installedAt) {
		t.Errorf("InstalledAt not preserved: want %v, got %v", installedAt, got.InstalledAt)
	}
}
