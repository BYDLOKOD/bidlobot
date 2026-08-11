package storage

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestDeferredQueue_EnqueueListByUser(t *testing.T) {
	store, err := NewBoltStore(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := NewDeferredRepo(store.DB())
	ctx := context.Background()

	// Two users, two jobs each.
	for _, tc := range []struct {
		userID int64
		url    string
	}{
		{100, "https://vt.tiktok.com/AAA"},
		{100, "https://vt.tiktok.com/BBB"},
		{200, "https://vt.tiktok.com/CCC"},
		{200, "https://vt.tiktok.com/DDD"},
	} {
		payload, _ := json.Marshal(TikTokPayload{URL: tc.url})
		if err := repo.Enqueue(ctx, DeferredJob{
			UserID:    tc.userID,
			Type:      DeferredTikTok,
			ChatID:    -100,
			MessageID: 1,
			Payload:   payload,
			CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond) // ensure distinct timestamps
	}

	// User 100 sees only their 2 jobs.
	jobs, err := repo.ListByUser(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("user 100: got %d jobs, want 2", len(jobs))
	}
	if jobs[0].Key == "" {
		t.Error("Key not populated")
	}

	// Verify payload round-trips.
	var p TikTokPayload
	json.Unmarshal(jobs[0].Payload, &p)
	if p.URL != "https://vt.tiktok.com/AAA" {
		t.Errorf("first job URL = %q, want AAA", p.URL)
	}

	// User 200 sees only their 2 jobs.
	jobs, err = repo.ListByUser(ctx, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("user 200: got %d jobs, want 2", len(jobs))
	}

	// Delete one.
	if err := repo.Delete(ctx, jobs[0].Key); err != nil {
		t.Fatal(err)
	}
	jobs, _ = repo.ListByUser(ctx, 200)
	if len(jobs) != 1 {
		t.Fatalf("after delete: got %d, want 1", len(jobs))
	}
}

func TestDeferredQueue_GarbageCollect(t *testing.T) {
	store, err := NewBoltStore(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := NewDeferredRepo(store.DB())
	ctx := context.Background()

	// Old job (3 days ago).
	repo.Enqueue(ctx, DeferredJob{
		UserID:    1,
		Type:      DeferredTikTok,
		CreatedAt: time.Now().UTC().Add(-3 * 24 * time.Hour),
	})
	// Fresh job (1 hour ago).
	repo.Enqueue(ctx, DeferredJob{
		UserID:    1,
		Type:      DeferredTikTok,
		CreatedAt: time.Now().UTC().Add(-time.Hour),
	})

	// GC entries older than 48h.
	n, err := repo.GarbageCollect(ctx, time.Now().UTC().Add(-DeferredTTL))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("GC removed %d, want 1", n)
	}

	jobs, _ := repo.ListByUser(ctx, 1)
	if len(jobs) != 1 {
		t.Fatalf("after GC: %d jobs, want 1", len(jobs))
	}
}

func TestDeferredQueue_Empty(t *testing.T) {
	store, err := NewBoltStore(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := NewDeferredRepo(store.DB())

	jobs, err := repo.ListByUser(context.Background(), 999)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("empty queue returned %d jobs", len(jobs))
	}
}
