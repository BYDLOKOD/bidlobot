package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/veschin/bidlobot/internal/games/hangman"
	"github.com/veschin/bidlobot/internal/storage"
)

func newHangmanRepo(t *testing.T) *storage.HangmanRepo {
	t.Helper()
	store := newTestStore(t)
	return storage.NewHangmanRepo(store.DB())
}

func TestHangmanGetRoundMissingReturnsErrNotFound(t *testing.T) {
	repo := newHangmanRepo(t)
	_, err := repo.GetRound(context.Background(), 1000)
	if !errors.Is(err, hangman.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestHangmanPutAndGetRoundRoundTrip(t *testing.T) {
	repo := newHangmanRepo(t)
	rec := hangman.Round{
		AbsChatID:  1000,
		Word:       "GOLANG",
		Used:       map[string]bool{"G": true, "O": true, "Z": true},
		WrongCount: 1,
		Active:     true,
		StartedAt:  time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC),
	}
	if err := repo.PutRound(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetRound(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got.Word != "GOLANG" || got.WrongCount != 1 || !got.Active {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if !got.Used["G"] || !got.Used["O"] || !got.Used["Z"] || got.Used["X"] {
		t.Errorf("used-letter set not round-tripped: %+v", got.Used)
	}
	if !got.StartedAt.Equal(rec.StartedAt) {
		t.Errorf("timestamp mismatch: %v vs %v", got.StartedAt, rec.StartedAt)
	}
}

func TestHangmanGetRoundNormalizesNilUsed(t *testing.T) {
	repo := newHangmanRepo(t)
	// A round serialized with a nil Used map must come back with an
	// empty (non-nil) map so callers never deref nil.
	if err := repo.PutRound(context.Background(), hangman.Round{
		AbsChatID: 1000, Word: "GO", Active: true, StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetRound(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got.Used == nil {
		t.Error("Used must be normalized to a non-nil map")
	}
}

func TestHangmanDeleteRound(t *testing.T) {
	repo := newHangmanRepo(t)
	if err := repo.PutRound(context.Background(), hangman.Round{
		AbsChatID: 1000, Word: "RUST", Used: map[string]bool{}, Active: true, StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteRound(context.Background(), 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetRound(context.Background(), 1000); !errors.Is(err, hangman.ErrNotFound) {
		t.Errorf("round should be gone, got %v", err)
	}
	if err := repo.DeleteRound(context.Background(), 1000); err != nil {
		t.Errorf("deleting missing round should be a no-op, got %v", err)
	}
}

func TestHangmanDeleteBeforeAnyWriteIsNoop(t *testing.T) {
	repo := newHangmanRepo(t)
	if err := repo.DeleteRound(context.Background(), 1000); err != nil {
		t.Errorf("delete before any write should not panic/error, got %v", err)
	}
}

func TestHangmanRoundsIsolatedByChat(t *testing.T) {
	repo := newHangmanRepo(t)
	if err := repo.PutRound(context.Background(), hangman.Round{AbsChatID: 1000, Word: "GO", Active: true, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := repo.PutRound(context.Background(), hangman.Round{AbsChatID: 2000, Word: "RUST", Active: true, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	r1, _ := repo.GetRound(context.Background(), 1000)
	r2, _ := repo.GetRound(context.Background(), 2000)
	if r1.Word != "GO" || r2.Word != "RUST" {
		t.Errorf("chat isolation broken: %+v %+v", r1, r2)
	}
}

func TestHangmanPutRoundRejectsZeroChatOrEmptyWord(t *testing.T) {
	repo := newHangmanRepo(t)
	if err := repo.PutRound(context.Background(), hangman.Round{Word: "GO", Active: true, StartedAt: time.Now()}); err == nil {
		t.Error("zero chat should be rejected")
	}
	if err := repo.PutRound(context.Background(), hangman.Round{AbsChatID: 1, Active: true, StartedAt: time.Now()}); err == nil {
		t.Error("empty word should be rejected")
	}
}
