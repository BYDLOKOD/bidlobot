package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/veschin/bidlobot/internal/games/dice"
	"github.com/veschin/bidlobot/internal/storage"
)

func newDiceRepo(t *testing.T) *storage.DiceRepo {
	t.Helper()
	store := newTestStore(t)
	return storage.NewDiceRepo(store.DB())
}

func TestDiceGetMissingReturnsErrNotFound(t *testing.T) {
	repo := newDiceRepo(t)
	_, err := repo.Get(context.Background(), 1000, dice.DefaultEmoji)
	if !errors.Is(err, dice.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDicePutAndGetRoundTrip(t *testing.T) {
	repo := newDiceRepo(t)
	rec := dice.Record{
		AbsChatID: 1000,
		Emoji:     dice.DefaultEmoji,
		Value:     6,
		UserID:    200,
		Username:  "alice",
		FirstName: "Alice",
		SetAt:     time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC),
	}
	if err := repo.Put(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(context.Background(), 1000, dice.DefaultEmoji)
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != 200 || got.Value != 6 || got.Username != "alice" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if !got.SetAt.Equal(rec.SetAt) {
		t.Errorf("timestamp mismatch: %v vs %v", got.SetAt, rec.SetAt)
	}
}

func TestDicePutOverwrites(t *testing.T) {
	repo := newDiceRepo(t)
	ts := time.Now().UTC()
	if err := repo.Put(context.Background(), dice.Record{
		AbsChatID: 1000, Emoji: dice.DefaultEmoji, Value: 4, UserID: 200, SetAt: ts,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Put(context.Background(), dice.Record{
		AbsChatID: 1000, Emoji: dice.DefaultEmoji, Value: 6, UserID: 300, SetAt: ts,
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.Get(context.Background(), 1000, dice.DefaultEmoji)
	if got.UserID != 300 || got.Value != 6 {
		t.Errorf("overwrite failed: %+v", got)
	}
}

func TestDiceSeparateChatsAndEmojisIsolated(t *testing.T) {
	repo := newDiceRepo(t)
	ts := time.Now().UTC()

	if err := repo.Put(context.Background(), dice.Record{
		AbsChatID: 1000, Emoji: dice.DefaultEmoji, Value: 6, UserID: 200, SetAt: ts,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Put(context.Background(), dice.Record{
		AbsChatID: 2000, Emoji: dice.DefaultEmoji, Value: 1, UserID: 300, SetAt: ts,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Put(context.Background(), dice.Record{
		AbsChatID: 1000, Emoji: "\U0001F3AF", Value: 3, UserID: 400, SetAt: ts,
	}); err != nil {
		t.Fatal(err)
	}

	r1, _ := repo.Get(context.Background(), 1000, dice.DefaultEmoji)
	r2, _ := repo.Get(context.Background(), 2000, dice.DefaultEmoji)
	r3, _ := repo.Get(context.Background(), 1000, "\U0001F3AF")
	if r1.UserID != 200 || r2.UserID != 300 || r3.UserID != 400 {
		t.Errorf("chat/emoji isolation broken: %+v %+v %+v", r1, r2, r3)
	}
}

func TestDicePutRejectsZeroChat(t *testing.T) {
	repo := newDiceRepo(t)
	err := repo.Put(context.Background(), dice.Record{Emoji: dice.DefaultEmoji, Value: 1, UserID: 1, SetAt: time.Now()})
	if err == nil {
		t.Fatal("zero chat should be rejected")
	}
}

func TestDicePutRejectsEmptyEmoji(t *testing.T) {
	repo := newDiceRepo(t)
	err := repo.Put(context.Background(), dice.Record{AbsChatID: 1, Value: 1, UserID: 1, SetAt: time.Now()})
	if err == nil {
		t.Fatal("empty emoji should be rejected")
	}
}
