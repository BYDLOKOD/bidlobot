package storage_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/veschin/bidlobot/internal/domain/reputation"
	"github.com/veschin/bidlobot/internal/storage"
)

func newReputationRepo(t *testing.T) *storage.ReputationRepo {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := storage.NewBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return storage.NewReputationRepo(s.DB())
}

func TestReputationApplyPraise(t *testing.T) {
	repo := newReputationRepo(t)
	ctx := context.Background()

	res, err := repo.Apply(ctx, 100, 1, 2, reputation.KindPraise, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.ActorBalance != 9 {
		t.Errorf("actor: want 9, got %d", res.ActorBalance)
	}
	if res.TargetBalance != 13 {
		t.Errorf("target: want 13, got %d", res.TargetBalance)
	}
}

func TestReputationApplyPraiseAdminTarget(t *testing.T) {
	repo := newReputationRepo(t)
	ctx := context.Background()

	res, err := repo.Apply(ctx, 101, 1, 2, reputation.KindPraise, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.ActorBalance != 9 {
		t.Errorf("actor: want 9, got %d", res.ActorBalance)
	}
	if res.TargetBalance != 26 {
		t.Errorf("target: want 26, got %d", res.TargetBalance)
	}
}

func TestReputationApplyRoast(t *testing.T) {
	repo := newReputationRepo(t)
	ctx := context.Background()

	res, err := repo.Apply(ctx, 102, 1, 2, reputation.KindRoast, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.ActorBalance != 9 {
		t.Errorf("actor: want 9, got %d", res.ActorBalance)
	}
	if res.TargetBalance != 9 {
		t.Errorf("target: want 9, got %d", res.TargetBalance)
	}
}

func TestReputationApplySelfTarget(t *testing.T) {
	repo := newReputationRepo(t)
	ctx := context.Background()

	_, err := repo.Apply(ctx, 103, 1, 1, reputation.KindPraise, false, false)
	if err == nil {
		t.Fatal("expected error for self-target")
	}
}

func TestReputationBalanceDefault(t *testing.T) {
	repo := newReputationRepo(t)
	ctx := context.Background()

	bal, err := repo.Balance(ctx, 200, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if bal != 10 {
		t.Errorf("regular default: want 10, got %d", bal)
	}

	bal, err = repo.Balance(ctx, 200, 2, true)
	if err != nil {
		t.Fatal(err)
	}
	if bal != 20 {
		t.Errorf("admin default: want 20, got %d", bal)
	}
}

func TestReputationLeaderboardSorting(t *testing.T) {
	repo := newReputationRepo(t)
	ctx := context.Background()
	const chatID int64 = 300

	// Create several users with different balances using direct operations.
	// User 1 praises user 100: 1->9, 100->13
	_, _ = repo.Apply(ctx, chatID, 1, 100, reputation.KindPraise, false, false)
	_, _ = repo.Apply(ctx, chatID, 2, 101, reputation.KindRoast, false, false)
	_, _ = repo.Apply(ctx, chatID, 3, 102, reputation.KindPraise, false, false)
	_, _ = repo.Apply(ctx, chatID, 4, 2, reputation.KindRoast, false, false)
	_, _ = repo.Apply(ctx, chatID, 2, 5, reputation.KindRoast, false, false)

	entries, err := repo.Leaderboard(ctx, chatID, 10)
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) == 0 {
		t.Fatal("leaderboard should not be empty")
	}

	// Verify descending balance order
	for i := 1; i < len(entries); i++ {
		if entries[i].Balance > entries[i-1].Balance {
			t.Fatalf("leaderboard not sorted: %+v before %+v", entries[i-1], entries[i])
		}
	}
}

func TestReputationLeaderboardLimit(t *testing.T) {
	repo := newReputationRepo(t)
	ctx := context.Background()
	const chatID int64 = 400

	// Create 5 users with praise operations
	for i := range 5 {
		userID := int64(i + 1)
		_, err := repo.Apply(ctx, chatID, userID, int64(100+i), reputation.KindPraise, false, false)
		if err != nil {
			t.Fatal(err)
		}
	}

	entries, err := repo.Leaderboard(ctx, chatID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("want 3 entries, got %d", len(entries))
	}
}
