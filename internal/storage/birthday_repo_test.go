package storage_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/veschin/bidlobot/internal/storage"
)

func newBirthdayRepo(t *testing.T) (*storage.BirthdayRepo, *storage.BoltStore) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "birthday.db")
	s, err := storage.NewBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return storage.NewBirthdayRepo(s.DB()), s
}

func TestBirthdayRepoMarkIsClear(t *testing.T) {
	repo, _ := newBirthdayRepo(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)

	if err := repo.MarkGreeted(ctx, 100, 1, "2026-10-27", now); err != nil {
		t.Fatalf("MarkGreeted: %v", err)
	}
	got, err := repo.IsGreeted(ctx, 100, 1, "2026-10-27")
	if err != nil {
		t.Fatalf("IsGreeted: %v", err)
	}
	if !got {
		t.Fatal("marker not found after MarkGreeted")
	}

	other, err := repo.IsGreeted(ctx, 100, 2, "2026-10-27")
	if err != nil {
		t.Fatalf("IsGreeted other: %v", err)
	}
	if other {
		t.Fatal("unrelated user reported as greeted")
	}

	if err := repo.ClearGreeted(ctx, 100, 1, "2026-10-27"); err != nil {
		t.Fatalf("ClearGreeted: %v", err)
	}
	got, err = repo.IsGreeted(ctx, 100, 1, "2026-10-27")
	if err != nil {
		t.Fatalf("IsGreeted after clear: %v", err)
	}
	if got {
		t.Fatal("marker survived ClearGreeted")
	}
}

func TestBirthdayRepoPrune(t *testing.T) {
	repo, _ := newBirthdayRepo(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	const day = "2026-10-27"

	if err := repo.MarkGreeted(ctx, 100, 1, day, now.Add(-49*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkGreeted(ctx, 100, 2, day, now.Add(-47*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkGreeted(ctx, 200, 3, day, now.Add(-49*time.Hour)); err != nil {
		t.Fatal(err)
	}

	if err := repo.Prune(ctx, 100, now.Add(-48*time.Hour)); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	old, _ := repo.IsGreeted(ctx, 100, 1, day)
	if old {
		t.Error("old marker in the pruned chat survived")
	}
	fresh, _ := repo.IsGreeted(ctx, 100, 2, day)
	if !fresh {
		t.Error("fresh marker in the pruned chat was removed")
	}
	otherChat, _ := repo.IsGreeted(ctx, 200, 3, day)
	if !otherChat {
		t.Error("marker in another chat was removed")
	}
}

func TestBirthdayRepoMigratesChatID(t *testing.T) {
	repo, store := newBirthdayRepo(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	const day = "2026-10-27"

	if err := repo.MarkGreeted(ctx, 100, 1, day, now); err != nil {
		t.Fatal(err)
	}
	report, err := storage.MigrateChatID(ctx, store.DB(), 100, 200)
	if err != nil {
		t.Fatalf("MigrateChatID: %v", err)
	}
	if report.BirthdayGreeted != 1 {
		t.Errorf("BirthdayGreeted = %d, want 1", report.BirthdayGreeted)
	}
	moved, _ := repo.IsGreeted(ctx, 200, 1, day)
	if !moved {
		t.Error("marker not found under the new chat id")
	}
	old, _ := repo.IsGreeted(ctx, 100, 1, day)
	if old {
		t.Error("marker still found under the old chat id")
	}
}
