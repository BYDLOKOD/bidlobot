package storage_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/veschin/bidlobot/internal/domain/pending"
	"github.com/veschin/bidlobot/internal/storage"
)

func newPendingRepo(t *testing.T) *storage.PendingRepo {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := storage.NewBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return storage.NewPendingRepo(st.DB())
}

func TestNewIDIsHex16(t *testing.T) {
	id, err := storage.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 16 {
		t.Fatalf("expected 16 hex chars, got %d (%q)", len(id), id)
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("non-hex char %q in %q", c, id)
		}
	}
}

func TestNewIDsAreDistinct(t *testing.T) {
	seen := make(map[string]struct{})
	for i := 0; i < 100; i++ {
		id, err := storage.NewID()
		if err != nil {
			t.Fatal(err)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id %q at iteration %d", id, i)
		}
		seen[id] = struct{}{}
	}
}

func TestPendingCreateAndGet(t *testing.T) {
	repo := newPendingRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()

	a := pending.Action{
		ID:           "abc123def4567890",
		Kind:         pending.KindWarn,
		AbsChatID:    100,
		ActorUserID:  111,
		TargetUserID: 222,
		Reason:       "spam links",
		CreatedAt:    now,
		ExpiresAt:    now.Add(5 * time.Minute),
	}
	if err := repo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}

	got, err := repo.Get(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != pending.KindWarn || got.Reason != "spam links" || got.TargetUserID != 222 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestPendingCreateRejectsEmptyID(t *testing.T) {
	repo := newPendingRepo(t)
	if err := repo.Create(context.Background(), pending.Action{
		ExpiresAt: time.Now().Add(time.Minute),
	}); err == nil {
		t.Fatal("expected error for empty ID")
	}
}

func TestPendingCreateRejectsZeroExpires(t *testing.T) {
	repo := newPendingRepo(t)
	if err := repo.Create(context.Background(), pending.Action{
		ID: "abc",
	}); err == nil {
		t.Fatal("expected error for zero ExpiresAt")
	}
}

func TestPendingCreateRejectsCollision(t *testing.T) {
	repo := newPendingRepo(t)
	ctx := context.Background()
	a := pending.Action{ID: "x", Kind: pending.KindBan, ExpiresAt: time.Now().Add(time.Minute)}
	if err := repo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := repo.Create(ctx, a); err == nil {
		t.Fatal("second Create with same ID should error")
	}
}

func TestPendingGetExpiredReturnsErrExpired(t *testing.T) {
	repo := newPendingRepo(t)
	ctx := context.Background()
	a := pending.Action{
		ID:        "expired",
		Kind:      pending.KindBan,
		ExpiresAt: time.Now().UTC().Add(-time.Second),
	}
	// bypass Create's "future expires" expectation by writing manually:
	// the Create method actually accepts past ExpiresAt (it only rejects zero),
	// so this remains a black-box test.
	if err := repo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	_, err := repo.Get(ctx, "expired")
	if err != pending.ErrExpired {
		t.Fatalf("expected ErrExpired, got %v", err)
	}
	// expired record must be gone after Get
	_, err = repo.Get(ctx, "expired")
	if err != pending.ErrNotFound {
		t.Fatalf("expected ErrNotFound after expiration cleanup, got %v", err)
	}
}

func TestPendingGetNotFound(t *testing.T) {
	repo := newPendingRepo(t)
	if _, err := repo.Get(context.Background(), "nope"); err != pending.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPendingDelete(t *testing.T) {
	repo := newPendingRepo(t)
	ctx := context.Background()
	a := pending.Action{ID: "dx", Kind: pending.KindBan, ExpiresAt: time.Now().Add(time.Minute)}
	_ = repo.Create(ctx, a)
	if err := repo.Delete(ctx, "dx"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, "dx"); err != pending.ErrNotFound {
		t.Fatalf("after delete should be ErrNotFound, got %v", err)
	}
}

func TestPendingGarbageCollect(t *testing.T) {
	repo := newPendingRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()

	_ = repo.Create(ctx, pending.Action{ID: "alive", Kind: pending.KindWarn, ExpiresAt: now.Add(time.Hour)})
	_ = repo.Create(ctx, pending.Action{ID: "dead1", Kind: pending.KindBan, ExpiresAt: now.Add(-time.Minute)})
	_ = repo.Create(ctx, pending.Action{ID: "dead2", Kind: pending.KindMute, ExpiresAt: now.Add(-time.Hour)})

	removed, err := repo.GarbageCollect(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("expected 2 removed, got %d", removed)
	}

	if _, err := repo.Get(ctx, "alive"); err != nil {
		t.Fatalf("alive entry should survive GC, got %v", err)
	}
	if _, err := repo.Get(ctx, "dead1"); err != pending.ErrNotFound {
		t.Fatalf("dead1 should be gone, got %v", err)
	}
}

func TestPendingConcurrentCreates(t *testing.T) {
	repo := newPendingRepo(t)
	ctx := context.Background()
	exp := time.Now().Add(time.Hour)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id, err := storage.NewID()
			if err != nil {
				t.Errorf("newID: %v", err)
				return
			}
			err = repo.Create(ctx, pending.Action{
				ID:        id,
				Kind:      pending.KindWarn,
				ExpiresAt: exp,
			})
			if err != nil {
				t.Errorf("create %d: %v", n, err)
			}
		}(i)
	}
	wg.Wait()

	// Sanity: GC with future "now" leaves all entries
	removed, _ := repo.GarbageCollect(ctx, time.Now().Add(-time.Hour))
	if removed != 0 {
		t.Fatalf("GC with past now should remove nothing, got %d", removed)
	}
}
