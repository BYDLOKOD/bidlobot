package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/veschin/bidlobot/internal/games/guess"
	"github.com/veschin/bidlobot/internal/storage"
)

func newGuessRepo(t *testing.T) *storage.GuessRepo {
	t.Helper()
	store := newTestStore(t)
	return storage.NewGuessRepo(store.DB())
}

func TestGuessGetRoundMissingReturnsErrNotFound(t *testing.T) {
	repo := newGuessRepo(t)
	_, err := repo.GetRound(context.Background(), 1000)
	if !errors.Is(err, guess.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestGuessPutAndGetRoundRoundTrip(t *testing.T) {
	repo := newGuessRepo(t)
	rec := guess.Round{
		AbsChatID: 1000,
		Secret:    42,
		Active:    true,
		StartedAt: time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC),
		Attempts:  3,
	}
	if err := repo.PutRound(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetRound(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got.Secret != 42 || !got.Active || got.Attempts != 3 {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if !got.StartedAt.Equal(rec.StartedAt) {
		t.Errorf("timestamp mismatch: %v vs %v", got.StartedAt, rec.StartedAt)
	}
}

func TestGuessDeleteRound(t *testing.T) {
	repo := newGuessRepo(t)
	if err := repo.PutRound(context.Background(), guess.Round{AbsChatID: 1000, Secret: 7, Active: true, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteRound(context.Background(), 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetRound(context.Background(), 1000); !errors.Is(err, guess.ErrNotFound) {
		t.Errorf("round should be gone, got %v", err)
	}
	// Deleting a missing round is a no-op.
	if err := repo.DeleteRound(context.Background(), 1000); err != nil {
		t.Errorf("deleting missing round should not error, got %v", err)
	}
}

func TestGuessDeleteRoundBeforeAnyWriteIsNoop(t *testing.T) {
	repo := newGuessRepo(t)
	// Bucket may not exist yet (no Put happened); Delete must not panic.
	if err := repo.DeleteRound(context.Background(), 1000); err != nil {
		t.Errorf("delete before any write should be a no-op, got %v", err)
	}
}

func TestGuessRoundsIsolatedByChat(t *testing.T) {
	repo := newGuessRepo(t)
	if err := repo.PutRound(context.Background(), guess.Round{AbsChatID: 1000, Secret: 11, Active: true, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := repo.PutRound(context.Background(), guess.Round{AbsChatID: 2000, Secret: 99, Active: true, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	r1, _ := repo.GetRound(context.Background(), 1000)
	r2, _ := repo.GetRound(context.Background(), 2000)
	if r1.Secret != 11 || r2.Secret != 99 {
		t.Errorf("chat isolation broken: %+v %+v", r1, r2)
	}
}

func TestGuessPutRoundRejectsZeroChat(t *testing.T) {
	repo := newGuessRepo(t)
	if err := repo.PutRound(context.Background(), guess.Round{Secret: 1, Active: true, StartedAt: time.Now()}); err == nil {
		t.Fatal("zero chat should be rejected")
	}
}

func TestGuessIncrementWinAndTop(t *testing.T) {
	repo := newGuessRepo(t)
	t0 := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)

	mustWin := func(userID int64, name string, at time.Time) {
		if err := repo.IncrementWin(context.Background(), guess.WinEntry{
			AbsChatID: 1000, UserID: userID, Username: name, FirstName: name, LastWonAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	mustWin(200, "alice", t0)
	mustWin(300, "bob", t0.Add(time.Minute))
	mustWin(200, "alice", t0.Add(2*time.Minute)) // alice -> 2

	top, err := repo.TopWins(context.Background(), 1000, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 2 || top[0].UserID != 200 || top[0].Wins != 2 || top[1].UserID != 300 {
		t.Errorf("leaderboard order wrong: %+v", top)
	}
}

func TestGuessTopWinsEmptyBeforeAnyWrite(t *testing.T) {
	repo := newGuessRepo(t)
	top, err := repo.TopWins(context.Background(), 1000, 5)
	if err != nil {
		t.Fatalf("TopWins before any write should not error, got %v", err)
	}
	if len(top) != 0 {
		t.Errorf("expected empty leaderboard, got %+v", top)
	}
}

func TestGuessWinsIsolatedByChat(t *testing.T) {
	repo := newGuessRepo(t)
	now := time.Now().UTC()
	if err := repo.IncrementWin(context.Background(), guess.WinEntry{AbsChatID: 1000, UserID: 200, LastWonAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repo.IncrementWin(context.Background(), guess.WinEntry{AbsChatID: 2000, UserID: 200, LastWonAt: now}); err != nil {
		t.Fatal(err)
	}
	t1, _ := repo.TopWins(context.Background(), 1000, 5)
	t2, _ := repo.TopWins(context.Background(), 2000, 5)
	if len(t1) != 1 || len(t2) != 1 {
		t.Errorf("win chat isolation broken: chat1=%+v chat2=%+v", t1, t2)
	}
}

func TestGuessIncrementWinRejectsZeroIDs(t *testing.T) {
	repo := newGuessRepo(t)
	if err := repo.IncrementWin(context.Background(), guess.WinEntry{UserID: 1}); err == nil {
		t.Error("zero chat should be rejected")
	}
	if err := repo.IncrementWin(context.Background(), guess.WinEntry{AbsChatID: 1}); err == nil {
		t.Error("zero user should be rejected")
	}
}
