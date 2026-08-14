package storage_test

import (
	"context"
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"

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
	if err := repo.FlushAtomic(ctx, batch, nil); err != nil {
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
	if err := repo.FlushAtomic(ctx, batch2, nil); err != nil {
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
	repo.FlushAtomic(ctx, batch, nil)

	list, err := repo.ListByChat(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 stats, got %d", len(list))
	}
}

func TestNewIDHex16(t *testing.T) {
	seen := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		id, err := storage.NewID()
		if err != nil {
			t.Fatal(err)
		}
		if len(id) != 16 {
			t.Fatalf("NewID() = %q, length %d, want 16 hex chars", id, len(id))
		}
		if _, err := hex.DecodeString(id); err != nil {
			t.Fatalf("NewID() = %q is not hex: %v", id, err)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("NewID() returned duplicate %q across 100 iterations", id)
		}
		seen[id] = struct{}{}
	}
}
