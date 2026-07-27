package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestAdmissionAttemptRepoPersistsCountsPerUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admission-attempts.db")
	store, err := NewBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewAdmissionAttemptRepo(store.DB())

	for want := uint64(1); want <= 3; want++ {
		got, err := repo.RecordUnauthorizedAdmission(context.Background(), 12345)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("attempt count = %d, want %d", got, want)
		}
	}
	if got, err := repo.RecordUnauthorizedAdmission(context.Background(), 67890); err != nil || got != 1 {
		t.Fatalf("second user count = %d, %v; want 1, nil", got, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = NewBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo = NewAdmissionAttemptRepo(store.DB())
	if got, err := repo.RecordUnauthorizedAdmission(context.Background(), 12345); err != nil || got != 4 {
		t.Fatalf("persisted count = %d, %v; want 4, nil", got, err)
	}
}
