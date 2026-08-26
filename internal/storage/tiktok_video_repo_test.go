package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestTikTokVideoRepo_RecordAndFind(t *testing.T) {
	store, err := NewBoltStore(filepath.Join(t.TempDir(), "tv.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := NewTikTokVideoRepo(store.DB())
	ctx := context.Background()

	// Miss before any record.
	if _, ok, err := repo.FindVideo(ctx, -100123, "111"); err != nil || ok {
		t.Fatalf("unrecorded video: ok=%v err=%v", ok, err)
	}

	if err := repo.RecordVideo(ctx, -100123, "111", 42); err != nil {
		t.Fatal(err)
	}
	msgID, ok, err := repo.FindVideo(ctx, -100123, "111")
	if err != nil || !ok || msgID != 42 {
		t.Fatalf("find = %d/%v/%v, want 42/true/nil", msgID, ok, err)
	}

	// Chat scoping: same video in another chat stays a miss.
	if _, ok, err := repo.FindVideo(ctx, -100999, "111"); err != nil || ok {
		t.Fatalf("other chat leaked: ok=%v err=%v", ok, err)
	}
	// Video scoping: another video in the same chat stays a miss.
	if _, ok, err := repo.FindVideo(ctx, -100123, "222"); err != nil || ok {
		t.Fatalf("other video leaked: ok=%v err=%v", ok, err)
	}

	// Re-repost overwrites to the newest message id.
	if err := repo.RecordVideo(ctx, -100123, "111", 77); err != nil {
		t.Fatal(err)
	}
	msgID, ok, err = repo.FindVideo(ctx, -100123, "111")
	if err != nil || !ok || msgID != 77 {
		t.Fatalf("find after re-repost = %d/%v/%v, want 77/true/nil", msgID, ok, err)
	}
}

func TestTikTokVideoRepo_SurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tv.db")
	store, err := NewBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := NewTikTokVideoRepo(store.DB()).RecordVideo(context.Background(), -100123, "111", 42); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = NewBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	msgID, ok, err := NewTikTokVideoRepo(store.DB()).FindVideo(context.Background(), -100123, "111")
	if err != nil || !ok || msgID != 42 {
		t.Fatalf("find after reopen = %d/%v/%v, want 42/true/nil", msgID, ok, err)
	}
}
