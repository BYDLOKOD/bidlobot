package reputation_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/veschin/bidlobot/internal/domain/reputation"
	"github.com/veschin/bidlobot/internal/storage"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "rep-test-*")
	if err != nil {
		os.Stderr.WriteString("reputation test: create temp dir: " + err.Error() + "\n")
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "test.db")
	bs, err := storage.NewBoltStore(path)
	if err != nil {
		os.Stderr.WriteString("reputation test: create bolt store: " + err.Error() + "\n")
		os.Exit(1)
	}
	defer bs.Close()

	reputation.SetStore(storage.NewReputationRepo(bs.DB()))

	os.Exit(m.Run())
}

func TestApplyPraiseDebitsActorCreditsTarget(t *testing.T) {
	ctx := context.Background()
	const absChat, actor, target int64 = 100, 1, 2

	res, err := reputation.Apply(ctx, absChat, actor, target, reputation.KindPraise, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.ActorBalance != 9 || res.TargetBalance != 13 {
		t.Fatalf("praise regular->regular: want 9/13, got %d/%d", res.ActorBalance, res.TargetBalance)
	}
}

func TestApplyPraiseAdminTargetGetsSix(t *testing.T) {
	ctx := context.Background()
	const absChat, actor, target int64 = 101, 1, 2

	res, err := reputation.Apply(ctx, absChat, actor, target, reputation.KindPraise, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.ActorBalance != 9 || res.TargetBalance != 26 {
		t.Fatalf("praise regular->admin: want 9/26, got %d/%d (admin starts at 20 +6)", res.ActorBalance, res.TargetBalance)
	}
}

func TestApplyRoastDebitsBoth(t *testing.T) {
	ctx := context.Background()
	const absChat, actor, target int64 = 102, 1, 2

	res, err := reputation.Apply(ctx, absChat, actor, target, reputation.KindRoast, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.ActorBalance != 9 || res.TargetBalance != 9 {
		t.Fatalf("roast regular->regular: want 9/9, got %d/%d", res.ActorBalance, res.TargetBalance)
	}
}

func TestApplySelfTargetReturnsValidationError(t *testing.T) {
	ctx := context.Background()
	const absChat, same int64 = 103, 1

	_, err := reputation.Apply(ctx, absChat, same, same, reputation.KindPraise, false, false)
	if err == nil {
		t.Fatal("self-target must return a validation error, got nil")
	}
	if !errors.Is(err, reputation.ErrSelfTarget) {
		t.Fatalf("expected ErrSelfTarget, got %v", err)
	}
}

func TestApplyInsufficientBalanceReturnsValidationError(t *testing.T) {
	ctx := context.Background()
	const absChat, actor, target int64 = 104, 1, 2

	for range 10 {
		_, _ = reputation.Apply(ctx, absChat, actor, target, reputation.KindRoast, false, false)
	}

	_, err := reputation.Apply(ctx, absChat, actor, target, reputation.KindRoast, false, false)
	if err == nil {
		t.Fatal("roast with zero actor balance must return validation error")
	}
	if !errors.Is(err, reputation.ErrInsufficientBalance) {
		t.Fatalf("expected ErrInsufficientBalance, got %v", err)
	}
}

func TestApplyZeroTargetBalanceRejectsRoast(t *testing.T) {
	ctx := context.Background()
	const absChat, actor, target int64 = 105, 1, 2

	for range 10 {
		_, _ = reputation.Apply(ctx, absChat, 3, target, reputation.KindRoast, false, false)
	}

	_, err := reputation.Apply(ctx, absChat, actor, target, reputation.KindRoast, false, false)
	if err == nil {
		t.Fatal("roast of zero-balance target must return validation error")
	}
	if !errors.Is(err, reputation.ErrTargetInsufficientBalance) {
		t.Fatalf("expected ErrTargetInsufficientBalance, got %v", err)
	}
}
