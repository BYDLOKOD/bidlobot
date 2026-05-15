package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/veschin/bidlobot/internal/domain/gracekick"
	"github.com/veschin/bidlobot/internal/storage"
)

func TestGraceKickRepoRoundTripChatIsolationAndDelete(t *testing.T) {
	repo := storage.NewGraceKickRepo(newTestStore(t).DB())
	ctx := context.Background()
	const chatA, chatB = int64(100), int64(200)
	now := time.Now().UTC().Truncate(time.Second)

	if recs, err := repo.ListByChat(ctx, chatA); err != nil || len(recs) != 0 {
		t.Fatalf("empty chat must list nothing, got %v err=%v", recs, err)
	}

	mk := func(chat, uid int64) gracekick.Record {
		return gracekick.Record{
			AbsChatID: chat, UserID: uid, Username: "u", FirstName: "N",
			TaggedAt: now, GraceDeadline: now.Add(72 * time.Hour),
		}
	}
	for _, r := range []gracekick.Record{mk(chatA, 1), mk(chatA, 2), mk(chatB, 9)} {
		if err := repo.Put(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	a, err := repo.ListByChat(ctx, chatA)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 2 {
		t.Fatalf("chatA must have exactly its own 2 tickets (prefix isolation), got %d", len(a))
	}
	b, err := repo.ListByChat(ctx, chatB)
	if err != nil || len(b) != 1 || b[0].UserID != 9 {
		t.Fatalf("chatB isolation broken: %+v err=%v", b, err)
	}
	if !a[0].GraceDeadline.Equal(now.Add(72 * time.Hour)) {
		t.Fatalf("deadline must round-trip, got %v", a[0].GraceDeadline)
	}

	// Idempotent re-Put refreshes in place (no duplicate).
	if err := repo.Put(ctx, mk(chatA, 1)); err != nil {
		t.Fatal(err)
	}
	if a2, _ := repo.ListByChat(ctx, chatA); len(a2) != 2 {
		t.Fatalf("re-Put must upsert, not duplicate, got %d", len(a2))
	}

	if err := repo.Delete(ctx, chatA, 1); err != nil {
		t.Fatal(err)
	}
	a3, _ := repo.ListByChat(ctx, chatA)
	if len(a3) != 1 || a3[0].UserID != 2 {
		t.Fatalf("after delete chatA must hold only user 2, got %+v", a3)
	}
	// Deleting a non-existent ticket is a no-op, not an error.
	if err := repo.Delete(ctx, chatA, 12345); err != nil {
		t.Fatalf("delete of missing ticket must be a no-op, got %v", err)
	}
}
