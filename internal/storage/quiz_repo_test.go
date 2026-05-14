package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/veschin/bidlobot/internal/games/quiz"
	"github.com/veschin/bidlobot/internal/storage"
)

func newQuizRepo(t *testing.T) *storage.QuizRepo {
	t.Helper()
	return storage.NewQuizRepo(newTestStore(t).DB())
}

func TestQuizGetMissingErrNotFound(t *testing.T) {
	repo := newQuizRepo(t)
	_, err := repo.GetEntry(context.Background(), 1000, 200)
	if !errors.Is(err, quiz.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestQuizIncrementCreatesAndUpdates(t *testing.T) {
	repo := newQuizRepo(t)
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)

	if err := repo.IncrementCorrect(context.Background(), quiz.Entry{
		AbsChatID: 1000, UserID: 200, Username: "alice", FirstName: "Alice", LastPlayedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetEntry(context.Background(), 1000, 200)
	if err != nil {
		t.Fatal(err)
	}
	if got.CorrectCount != 1 {
		t.Errorf("first call should set count to 1, got %d", got.CorrectCount)
	}

	// Second call bumps to 2; username/lastPlayed update
	later := now.Add(time.Hour)
	if err := repo.IncrementCorrect(context.Background(), quiz.Entry{
		AbsChatID: 1000, UserID: 200, Username: "alice2", LastPlayedAt: later,
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.GetEntry(context.Background(), 1000, 200)
	if got.CorrectCount != 2 {
		t.Errorf("second call should bump to 2, got %d", got.CorrectCount)
	}
	if got.Username != "alice2" {
		t.Errorf("username should update, got %q", got.Username)
	}
	if !got.LastPlayedAt.Equal(later) {
		t.Errorf("LastPlayedAt should advance, got %v", got.LastPlayedAt)
	}
}

func TestQuizIncrementRejectsZeroIDs(t *testing.T) {
	repo := newQuizRepo(t)
	if err := repo.IncrementCorrect(context.Background(), quiz.Entry{UserID: 1, LastPlayedAt: time.Now()}); err == nil {
		t.Error("zero AbsChatID must be rejected")
	}
	if err := repo.IncrementCorrect(context.Background(), quiz.Entry{AbsChatID: 1, LastPlayedAt: time.Now()}); err == nil {
		t.Error("zero UserID must be rejected")
	}
}

func TestQuizTopByChatSortedByCount(t *testing.T) {
	repo := newQuizRepo(t)
	now := time.Now().UTC()
	mustInc := func(uid int64, count int) {
		for i := 0; i < count; i++ {
			if err := repo.IncrementCorrect(context.Background(), quiz.Entry{
				AbsChatID: 1000, UserID: uid, LastPlayedAt: now,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	mustInc(200, 5)
	mustInc(300, 3)
	mustInc(400, 8)
	mustInc(500, 1)

	top, err := repo.TopByChat(context.Background(), 1000, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(top))
	}
	want := []int64{400, 200, 300, 500}
	for i, w := range want {
		if top[i].UserID != w {
			t.Errorf("position %d: expected user %d, got %d (counts=%d)",
				i, w, top[i].UserID, top[i].CorrectCount)
		}
	}
}

func TestQuizTopByChatLimit(t *testing.T) {
	repo := newQuizRepo(t)
	for uid := int64(1); uid <= 10; uid++ {
		repo.IncrementCorrect(context.Background(), quiz.Entry{
			AbsChatID: 1000, UserID: uid, LastPlayedAt: time.Now(),
		})
	}
	top, _ := repo.TopByChat(context.Background(), 1000, 3)
	if len(top) != 3 {
		t.Errorf("expected limit=3 to truncate, got %d", len(top))
	}
}

func TestQuizTopByChatTieBreakerOlderFirst(t *testing.T) {
	repo := newQuizRepo(t)
	t1 := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	if err := repo.IncrementCorrect(context.Background(), quiz.Entry{AbsChatID: 1000, UserID: 200, LastPlayedAt: t2}); err != nil {
		t.Fatal(err)
	}
	if err := repo.IncrementCorrect(context.Background(), quiz.Entry{AbsChatID: 1000, UserID: 300, LastPlayedAt: t1}); err != nil {
		t.Fatal(err)
	}
	top, _ := repo.TopByChat(context.Background(), 1000, 5)
	if top[0].UserID != 300 {
		t.Errorf("tie-broken on older LastPlayedAt; expected user 300 first, got %d", top[0].UserID)
	}
}

func TestQuizSeparateChatsIsolated(t *testing.T) {
	repo := newQuizRepo(t)
	repo.IncrementCorrect(context.Background(), quiz.Entry{AbsChatID: 1000, UserID: 200, LastPlayedAt: time.Now()})
	repo.IncrementCorrect(context.Background(), quiz.Entry{AbsChatID: 2000, UserID: 200, LastPlayedAt: time.Now()})

	top1, _ := repo.TopByChat(context.Background(), 1000, 5)
	top2, _ := repo.TopByChat(context.Background(), 2000, 5)
	if len(top1) != 1 || len(top2) != 1 {
		t.Errorf("chats should be isolated; got %d/%d", len(top1), len(top2))
	}
}
