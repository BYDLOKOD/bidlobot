package storage_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/veschin/bidlobot/internal/domain/moderation"
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
