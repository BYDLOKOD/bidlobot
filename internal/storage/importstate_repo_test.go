package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/veschin/bidlobot/internal/domain/dmsession"
	"github.com/veschin/bidlobot/internal/storage"
)

func TestImportStateSetGetClear(t *testing.T) {
	repo := storage.NewImportStateRepo(newTestStore(t).DB())
	ctx := context.Background()
	const admin = int64(42)

	if _, err := repo.Get(ctx, admin); !errors.Is(err, dmsession.ErrNoImportAwait) {
		t.Fatalf("missing state must be ErrNoImportAwait, got %v", err)
	}

	now := time.Now().UTC()
	in := dmsession.ImportState{
		AdminUserID: admin,
		AbsChatID:   1001234567890,
		StartedAt:   now,
		ExpiresAt:   now.Add(dmsession.ImportAwaitTTL),
	}
	if err := repo.Set(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if got.AdminUserID != admin || got.AbsChatID != in.AbsChatID {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	if err := repo.Clear(ctx, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, admin); !errors.Is(err, dmsession.ErrNoImportAwait) {
		t.Fatalf("after clear must be ErrNoImportAwait, got %v", err)
	}
}

func TestImportStateLazyExpiry(t *testing.T) {
	repo := storage.NewImportStateRepo(newTestStore(t).DB())
	ctx := context.Background()
	const admin = int64(7)

	past := time.Now().UTC().Add(-time.Hour)
	if err := repo.Set(ctx, dmsession.ImportState{
		AdminUserID: admin,
		AbsChatID:   55,
		StartedAt:   past.Add(-time.Minute),
		ExpiresAt:   past, // already expired
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, admin); !errors.Is(err, dmsession.ErrNoImportAwait) {
		t.Fatalf("expired state must read as ErrNoImportAwait, got %v", err)
	}
	// Expired row must be evicted: a subsequent Get still reports absent
	// (and the underlying key is gone, not just hidden).
	if _, err := repo.Get(ctx, admin); !errors.Is(err, dmsession.ErrNoImportAwait) {
		t.Fatalf("second Get after expiry must still be ErrNoImportAwait, got %v", err)
	}
}

func TestImportStateDefaultsTTL(t *testing.T) {
	repo := storage.NewImportStateRepo(newTestStore(t).DB())
	ctx := context.Background()
	const admin = int64(9)

	// Zero StartedAt/ExpiresAt must be filled by Set (now / now+TTL).
	if err := repo.Set(ctx, dmsession.ImportState{AdminUserID: admin, AbsChatID: 3}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if got.StartedAt.IsZero() || got.ExpiresAt.IsZero() {
		t.Fatalf("Set must default StartedAt/ExpiresAt, got %+v", got)
	}
	if !got.ExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("defaulted ExpiresAt must be in the future, got %v", got.ExpiresAt)
	}
}
