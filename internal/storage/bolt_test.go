package storage_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/veschin/bidlobot/internal/domain/moderation"
	"github.com/veschin/bidlobot/internal/domain/profile"
	"github.com/veschin/bidlobot/internal/domain/stats"
	"github.com/veschin/bidlobot/internal/storage"
)

func newTestStore(t *testing.T) *storage.BoltStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := storage.NewBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestProfileCRUD(t *testing.T) {
	store := newTestStore(t)
	repo := storage.NewProfileRepo(store.DB())
	ctx := context.Background()

	p := &profile.Profile{
		UserID:     111,
		ChatID:     1001234567890,
		Stack:      []string{"Go", "PostgreSQL"},
		Experience: []profile.ExpEntry{{Title: "Backend Developer", Period: "present"}},
		Username:   "testuser",
		FirstName:  "Test",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	if err := repo.Create(ctx, p); err != nil {
		t.Fatal("create:", err)
	}

	err := repo.Create(ctx, p)
	if err != profile.ErrExists {
		t.Fatal("expected ErrExists, got:", err)
	}

	got, err := repo.Get(ctx, 111, 1001234567890)
	if err != nil {
		t.Fatal("get:", err)
	}
	if len(got.Stack) != 2 || got.Stack[0] != "Go" || got.Stack[1] != "PostgreSQL" {
		t.Fatal("wrong stack:", got.Stack)
	}

	got.Stack = []string{"Rust", "Go"}
	if err := repo.Update(ctx, got); err != nil {
		t.Fatal("update:", err)
	}
	got2, _ := repo.Get(ctx, 111, 1001234567890)
	if len(got2.Stack) != 2 || got2.Stack[0] != "Rust" || got2.Stack[1] != "Go" {
		t.Fatal("update not persisted")
	}

	_, err = repo.Get(ctx, 999, 1001234567890)
	if err != profile.ErrNotFound {
		t.Fatal("expected ErrNotFound, got:", err)
	}
}

func TestProfileListByChat(t *testing.T) {
	store := newTestStore(t)
	repo := storage.NewProfileRepo(store.DB())
	ctx := context.Background()
	chatID := int64(1001234567890)
	now := time.Now()

	for _, uid := range []int64{111, 222, 333} {
		repo.Create(ctx, &profile.Profile{
			UserID: uid, ChatID: chatID, Stack: []string{"Go"}, Experience: []profile.ExpEntry{{Title: "Dev", Period: "present"}},
			Username: "user" + string(rune('A'+uid%26)), FirstName: "U",
			CreatedAt: now, UpdatedAt: now,
		})
	}
	repo.Create(ctx, &profile.Profile{
		UserID: 111, ChatID: 9999999999, Stack: []string{"Rust"}, Experience: []profile.ExpEntry{{Title: "Dev", Period: "present"}},
		Username: "testuser", FirstName: "U",
		CreatedAt: now, UpdatedAt: now,
	})

	list, err := repo.ListByChat(ctx, chatID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 profiles in chat, got %d", len(list))
	}
}

func TestProfileListByUser(t *testing.T) {
	store := newTestStore(t)
	repo := storage.NewProfileRepo(store.DB())
	ctx := context.Background()
	now := time.Now()

	for _, chatID := range []int64{100, 200, 300} {
		repo.Create(ctx, &profile.Profile{
			UserID: 111, ChatID: chatID, Stack: []string{"Go"}, Experience: []profile.ExpEntry{{Title: "Dev", Period: "present"}},
			Username: "user1", FirstName: "U",
			CreatedAt: now, UpdatedAt: now,
		})
	}

	list, err := repo.ListByUser(ctx, 111)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 profiles for user, got %d", len(list))
	}
}

func TestProfileGetByUsername(t *testing.T) {
	store := newTestStore(t)
	repo := storage.NewProfileRepo(store.DB())
	ctx := context.Background()
	now := time.Now()

	repo.Create(ctx, &profile.Profile{
		UserID: 111, ChatID: 100, Stack: []string{"Go"}, Experience: []profile.ExpEntry{{Title: "Dev", Period: "present"}},
		Username: "TestUser", FirstName: "Test",
		CreatedAt: now, UpdatedAt: now,
	})

	got, err := repo.GetByUsername(ctx, 100, "testuser")
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != 111 {
		t.Fatal("wrong user")
	}

	_, err = repo.GetByUsername(ctx, 100, "nonexistent")
	if err != profile.ErrNotFound {
		t.Fatal("expected ErrNotFound")
	}
}

func TestProfileUpdateUsernameAll(t *testing.T) {
	store := newTestStore(t)
	repo := storage.NewProfileRepo(store.DB())
	ctx := context.Background()
	now := time.Now()

	for _, chatID := range []int64{100, 200} {
		repo.Create(ctx, &profile.Profile{
			UserID: 111, ChatID: chatID, Stack: []string{"Go"}, Experience: []profile.ExpEntry{{Title: "Dev", Period: "present"}},
			Username: "oldname", FirstName: "U",
			CreatedAt: now, UpdatedAt: now,
		})
	}

	if err := repo.UpdateUsernameAll(ctx, 111, "newname"); err != nil {
		t.Fatal(err)
	}

	for _, chatID := range []int64{100, 200} {
		p, _ := repo.Get(ctx, 111, chatID)
		if p.Username != "newname" {
			t.Fatalf("chat %d: username not updated", chatID)
		}
	}
}

func TestStatsFlushAndGet(t *testing.T) {
	store := newTestStore(t)
	repo := storage.NewStatsRepo(store.DB())
	ctx := context.Background()
	now := time.Now()

	batch := map[stats.FlushKey]*stats.FlushDelta{
		{UserID: 111, AbsChatID: 100}: {CountDelta: 5, FirstSeen: now, LastSeen: now},
		{UserID: 222, AbsChatID: 100}: {CountDelta: 3, FirstSeen: now, LastSeen: now},
	}
	if err := repo.Flush(ctx, batch); err != nil {
		t.Fatal("flush:", err)
	}

	s, err := repo.Get(ctx, 111, 100)
	if err != nil {
		t.Fatal("get:", err)
	}
	if s.MessageCount != 5 {
		t.Fatal("wrong count:", s.MessageCount)
	}

	batch2 := map[stats.FlushKey]*stats.FlushDelta{
		{UserID: 111, AbsChatID: 100}: {CountDelta: 10, FirstSeen: now, LastSeen: now.Add(time.Hour)},
	}
	if err := repo.Flush(ctx, batch2); err != nil {
		t.Fatal("flush2:", err)
	}

	s2, _ := repo.Get(ctx, 111, 100)
	if s2.MessageCount != 15 {
		t.Fatalf("expected 15, got %d", s2.MessageCount)
	}
}

func TestStatsListByChat(t *testing.T) {
	store := newTestStore(t)
	repo := storage.NewStatsRepo(store.DB())
	ctx := context.Background()
	now := time.Now()

	batch := map[stats.FlushKey]*stats.FlushDelta{
		{UserID: 111, AbsChatID: 100}: {CountDelta: 5, FirstSeen: now, LastSeen: now},
		{UserID: 222, AbsChatID: 100}: {CountDelta: 3, FirstSeen: now, LastSeen: now},
		{UserID: 333, AbsChatID: 999}: {CountDelta: 1, FirstSeen: now, LastSeen: now},
	}
	repo.Flush(ctx, batch)

	list, err := repo.ListByChat(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 stats, got %d", len(list))
	}
}

func TestWarningAtomicCreate(t *testing.T) {
	store := newTestStore(t)
	repo := storage.NewWarnRepo(store.DB())
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		w := &moderation.Warning{
			ID: uuid.NewString(), TargetUserID: 222, ChatID: 100,
			IssuerUserID: 111, Reason: "spam", Timestamp: time.Now(),
		}
		count, err := repo.CreateWarning(ctx, w)
		if err != nil {
			t.Fatal("warn:", err)
		}
		if count != i+1 {
			t.Fatalf("warn %d: expected count %d, got %d", i, i+1, count)
		}
	}
}

func TestWarningClear(t *testing.T) {
	store := newTestStore(t)
	repo := storage.NewWarnRepo(store.DB())
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		repo.CreateWarning(ctx, &moderation.Warning{
			ID: uuid.NewString(), TargetUserID: 222, ChatID: 100,
			IssuerUserID: 111, Timestamp: time.Now(),
		})
	}

	if err := repo.ClearWarnings(ctx, 222, 100); err != nil {
		t.Fatal(err)
	}

	count, _ := repo.CountActive(ctx, 222, 100)
	if count != 0 {
		t.Fatalf("expected 0 active after clear, got %d", count)
	}

	w := &moderation.Warning{
		ID: uuid.NewString(), TargetUserID: 222, ChatID: 100,
		IssuerUserID: 111, Timestamp: time.Now(),
	}
	newCount, _ := repo.CreateWarning(ctx, w)
	if newCount != 1 {
		t.Fatalf("expected count 1 after clear+new, got %d", newCount)
	}
}
