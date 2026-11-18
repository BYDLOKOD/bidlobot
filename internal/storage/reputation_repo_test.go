package storage_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/domain/reputation"
	"github.com/veschin/bidlobot/internal/storage"
)

func newRepTestStore(t *testing.T) (*storage.ReputationRepo, *bolt.DB, func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := storage.NewBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	return storage.NewReputationRepo(s.DB()), s.DB(), func() { _ = s.Close() }
}

func repKeyFor(chatID, userID int64) []byte {
	if chatID < 0 {
		chatID = -chatID
	}
	return []byte(fmt.Sprintf("r:%020d:%020d", chatID, userID))
}

// putLegacyBalance writes a real-format v1 reputation row directly, the
// way an older binary stored it.
func putLegacyBalance(t *testing.T, db *bolt.DB, chatID, userID int64, balance int) {
	t.Helper()
	err := db.Update(func(tx *bolt.Tx) error {
		bkt, err := tx.CreateBucketIfNotExists([]byte("reputation"))
		if err != nil {
			return err
		}
		return bkt.Put(repKeyFor(chatID, userID), []byte(fmt.Sprintf(`{"balance":%d}`, balance)))
	})
	if err != nil {
		t.Fatal(err)
	}
}

func mustWallet(t *testing.T, repo *storage.ReputationRepo, chatID, userID int64) reputation.Account {
	t.Helper()
	account, err := repo.Wallet(context.Background(), chatID, userID)
	if err != nil {
		t.Fatalf("Wallet: %v", err)
	}
	return account
}

func settle(t *testing.T, repo *storage.ReputationRepo, req reputation.SocialRequest) reputation.Result {
	t.Helper()
	if req.Now.IsZero() {
		req.Now = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	}
	result, err := repo.Settle(context.Background(), req)
	if err != nil {
		t.Fatalf("Settle(%+v): %v", req, err)
	}
	return result
}

func TestReputationWalletDefaults(t *testing.T) {
	repo, _, cleanup := newRepTestStore(t)
	defer cleanup()

	account := mustWallet(t, repo, -100, 1)
	if account.Available != 10 || account.Reserved != 0 || account.Score != 0 {
		t.Fatalf("new member defaults = %+v, want available 10 score 0", account)
	}
	// A negative Telegram chat id and its normalized form address the
	// same wallet.
	if _, err := repo.Settle(context.Background(), reputation.SocialRequest{
		ChatID: -100, ActorID: 1, TargetID: 2, Kind: reputation.KindPraise,
		Source: reputation.SourceSlash, SourceMessageID: 1,
		Now: time.Date(2026, 11, 18, 1, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	normalized := mustWallet(t, repo, 100, 2)
	if normalized.Available != 13 || normalized.Score != 3 {
		t.Fatalf("normalized chat lookup = %+v", normalized)
	}
}

func TestReputationSettlePraiseAndAdminParity(t *testing.T) {
	repo, _, cleanup := newRepTestStore(t)
	defer cleanup()

	result := settle(t, repo, reputation.SocialRequest{
		ChatID: 100, ActorID: 1, TargetID: 2, Kind: reputation.KindPraise,
		Source: reputation.SourceSlash, SourceMessageID: 10,
	})
	if result.Actor.Available != 9 || result.Actor.Score != 0 {
		t.Fatalf("actor = %+v, want 9/0", result.Actor)
	}
	if result.Target.Available != 13 || result.Target.Score != 3 {
		t.Fatalf("target = %+v, want 13/3", result.Target)
	}
	if result.ActorWalletDelta != -1 || result.TargetWalletDelta != 3 || result.TargetScoreDelta != 3 {
		t.Fatalf("deltas = %+v", result)
	}

	// There is no administrator flag in the contract: both members get
	// exactly the same payout.
	next := settle(t, repo, reputation.SocialRequest{
		ChatID: 100, ActorID: 3, TargetID: 4, Kind: reputation.KindPraise,
		Source: reputation.SourceSlash, SourceMessageID: 11,
	})
	if next.Target.Available != 13 || next.Target.Score != 3 {
		t.Fatalf("second member payout = %+v", next.Target)
	}
}

func TestReputationSettleRoast(t *testing.T) {
	repo, _, cleanup := newRepTestStore(t)
	defer cleanup()

	settle(t, repo, reputation.SocialRequest{
		ChatID: 100, ActorID: 1, TargetID: 2, Kind: reputation.KindPraise,
		Source: reputation.SourceSlash, SourceMessageID: 10,
	})
	result := settle(t, repo, reputation.SocialRequest{
		ChatID: 100, ActorID: 3, TargetID: 2, Kind: reputation.KindRoast,
		Source: reputation.SourceSlash, SourceMessageID: 11,
	})
	if result.Actor.Available != 9 || result.Actor.Score != 0 {
		t.Fatalf("roast actor = %+v", result.Actor)
	}
	if result.Target.Available != 12 || result.Target.Score != 2 {
		t.Fatalf("roast target = %+v, want 12/2", result.Target)
	}
	if result.TargetWalletDelta != -1 || result.TargetScoreDelta != -1 {
		t.Fatalf("roast deltas = %+v", result)
	}
}

func TestReputationRoastCannotConsumeReservations(t *testing.T) {
	repo, db, cleanup := newRepTestStore(t)
	defer cleanup()

	settle(t, repo, reputation.SocialRequest{
		ChatID: 100, ActorID: 1, TargetID: 2, Kind: reputation.KindPraise,
		Source: reputation.SourceSlash, SourceMessageID: 1,
	})
	// Move the target's whole spendable wallet into a reservation.
	if err := db.Update(func(tx *bolt.Tx) error {
		return storage.AdjustReputationWallet(tx, 100, 2, -13, 13)
	}); err != nil {
		t.Fatal(err)
	}

	result := settle(t, repo, reputation.SocialRequest{
		ChatID: 100, ActorID: 3, TargetID: 2, Kind: reputation.KindRoast,
		Source: reputation.SourceSlash, SourceMessageID: 2,
	})
	if result.Target.Available != 0 || result.Target.Reserved != 13 {
		t.Fatalf("roast consumed reserved funds: %+v", result.Target)
	}
	if result.TargetWalletDelta != 0 {
		t.Fatalf("reserved-only roast wallet delta = %d, want 0", result.TargetWalletDelta)
	}
	if result.Target.Score != 2 || result.TargetScoreDelta != -1 {
		t.Fatalf("reserved-only roast score = %+v", result.Target)
	}
}

func TestReputationRoastScoreFloor(t *testing.T) {
	repo, _, cleanup := newRepTestStore(t)
	defer cleanup()

	// Target starts with score 0; a roast cannot make it negative.
	result := settle(t, repo, reputation.SocialRequest{
		ChatID: 100, ActorID: 1, TargetID: 2, Kind: reputation.KindRoast,
		Source: reputation.SourceSlash, SourceMessageID: 1,
	})
	if result.Target.Score != 0 || result.TargetScoreDelta != 0 {
		t.Fatalf("floor broken: %+v", result.Target)
	}
}

func TestReputationValidationAndLimits(t *testing.T) {
	t.Run("self target", func(t *testing.T) {
		repo, _, cleanup := newRepTestStore(t)
		defer cleanup()
		_, err := repo.Settle(context.Background(), reputation.SocialRequest{
			ChatID: 100, ActorID: 1, TargetID: 1, Kind: reputation.KindPraise,
		})
		if !errors.Is(err, reputation.ErrSelfTarget) {
			t.Fatalf("err = %v, want ErrSelfTarget", err)
		}
	})

	t.Run("insufficient sender", func(t *testing.T) {
		repo, db, cleanup := newRepTestStore(t)
		defer cleanup()
		if err := db.Update(func(tx *bolt.Tx) error {
			return storage.AdjustReputationWallet(tx, 100, 1, -10, 0)
		}); err != nil {
			t.Fatal(err)
		}
		_, err := repo.Settle(context.Background(), reputation.SocialRequest{
			ChatID: 100, ActorID: 1, TargetID: 2, Kind: reputation.KindPraise,
		})
		if !errors.Is(err, reputation.ErrInsufficientBalance) {
			t.Fatalf("err = %v, want ErrInsufficientBalance", err)
		}
		target := mustWallet(t, repo, 100, 2)
		if target.Available != 10 || target.Score != 0 {
			t.Fatalf("failed settlement mutated target: %+v", target)
		}
	})

	t.Run("one settlement per unordered pair per day", func(t *testing.T) {
		repo, _, cleanup := newRepTestStore(t)
		defer cleanup()
		settle(t, repo, reputation.SocialRequest{
			ChatID: 100, ActorID: 1, TargetID: 2, Kind: reputation.KindPraise,
			Source: reputation.SourceSlash, SourceMessageID: 1,
		})
		// The reverse direction is the same unordered pair.
		_, err := repo.Settle(context.Background(), reputation.SocialRequest{
			ChatID: 100, ActorID: 2, TargetID: 1, Kind: reputation.KindPraise,
			Source: reputation.SourceSlash, SourceMessageID: 2,
			Now: time.Date(2026, 11, 18, 13, 0, 0, 0, time.UTC),
		})
		if !errors.Is(err, reputation.ErrPairLimit) {
			t.Fatalf("err = %v, want ErrPairLimit", err)
		}
	})

	t.Run("three outgoing settlements per sender per day", func(t *testing.T) {
		repo, _, cleanup := newRepTestStore(t)
		defer cleanup()
		for i, target := range []int64{2, 3, 4} {
			settle(t, repo, reputation.SocialRequest{
				ChatID: 100, ActorID: 1, TargetID: target, Kind: reputation.KindPraise,
				Source: reputation.SourceSlash, SourceMessageID: i + 1,
			})
		}
		_, err := repo.Settle(context.Background(), reputation.SocialRequest{
			ChatID: 100, ActorID: 1, TargetID: 5, Kind: reputation.KindRoast,
			Source: reputation.SourceSlash, SourceMessageID: 4,
		})
		if !errors.Is(err, reputation.ErrSenderLimit) {
			t.Fatalf("err = %v, want ErrSenderLimit", err)
		}
		// A new UTC day resets both shared limits.
		next := settle(t, repo, reputation.SocialRequest{
			ChatID: 100, ActorID: 1, TargetID: 5, Kind: reputation.KindRoast,
			Source: reputation.SourceSlash, SourceMessageID: 5,
			Now: time.Date(2026, 11, 19, 0, 5, 0, 0, time.UTC),
		})
		if next.Actor.Available != 6 {
			t.Fatalf("next-day actor wallet = %d, want 6", next.Actor.Available)
		}
	})

	t.Run("roast shares the sender budget", func(t *testing.T) {
		repo, _, cleanup := newRepTestStore(t)
		defer cleanup()
		settle(t, repo, reputation.SocialRequest{
			ChatID: 100, ActorID: 1, TargetID: 2, Kind: reputation.KindPraise,
			Source: reputation.SourceSlash, SourceMessageID: 1,
		})
		settle(t, repo, reputation.SocialRequest{
			ChatID: 100, ActorID: 1, TargetID: 3, Kind: reputation.KindRoast,
			Source: reputation.SourceSlash, SourceMessageID: 2,
		})
		settle(t, repo, reputation.SocialRequest{
			ChatID: 100, ActorID: 1, TargetID: 4, Kind: reputation.KindPraise,
			Source: reputation.SourceSlash, SourceMessageID: 3,
		})
		_, err := repo.Settle(context.Background(), reputation.SocialRequest{
			ChatID: 100, ActorID: 1, TargetID: 5, Kind: reputation.KindPraise,
			Source: reputation.SourceSlash, SourceMessageID: 4,
		})
		if !errors.Is(err, reputation.ErrSenderLimit) {
			t.Fatalf("err = %v, want ErrSenderLimit", err)
		}
	})
}

func TestReputationSourceDeduplication(t *testing.T) {
	repo, _, cleanup := newRepTestStore(t)
	defer cleanup()

	req := reputation.SocialRequest{
		ChatID: 100, ActorID: 1, TargetID: 2, Kind: reputation.KindPraise,
		Source: reputation.SourceReaction, SourceMessageID: 77,
	}
	settle(t, repo, req)
	_, err := repo.Settle(context.Background(), req)
	if !errors.Is(err, reputation.ErrDuplicateSource) {
		t.Fatalf("re-add err = %v, want ErrDuplicateSource", err)
	}
	// The same actor on a different source message is a different event;
	// the pair limit still stops a same-day second settlement.
	req.SourceMessageID = 78
	_, err = repo.Settle(context.Background(), req)
	if !errors.Is(err, reputation.ErrPairLimit) {
		t.Fatalf("new source same pair err = %v, want ErrPairLimit", err)
	}
	// A different actor on the same message is not the same source event.
	other := req
	other.ActorID = 3
	other.TargetID = 2
	other.SourceMessageID = 77
	settle(t, repo, other)
}

func TestAdjustReputationWalletTransactionally(t *testing.T) {
	repo, db, cleanup := newRepTestStore(t)
	defer cleanup()

	err := db.Update(func(tx *bolt.Tx) error {
		account, err := storage.ReputationWallet(tx, -100, 7)
		if err != nil {
			return err
		}
		if account.Available != 10 || account.Score != 0 {
			return fmt.Errorf("default = %+v", account)
		}
		if err := storage.AdjustReputationWallet(tx, -100, 7, 5, 2); err != nil {
			return err
		}
		account, err = storage.ReputationWallet(tx, 100, 7)
		if err != nil {
			return err
		}
		if account.Available != 15 || account.Reserved != 2 || account.Score != 0 {
			return fmt.Errorf("adjusted = %+v", account)
		}
		if err := storage.AdjustReputationWallet(tx, 100, 7, -1, -1); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := mustWallet(t, repo, 100, 7); got.Available != 14 || got.Reserved != 1 || got.Score != 0 {
		t.Fatalf("after tx = %+v", got)
	}

	for name, adjust := range map[string][2]int{
		"available below zero": {-15, 0},
		"reserved below zero":  {0, -2},
	} {
		t.Run(name, func(t *testing.T) {
			err := db.Update(func(tx *bolt.Tx) error {
				return storage.AdjustReputationWallet(tx, 100, 7, adjust[0], adjust[1])
			})
			if name == "available below zero" && !errors.Is(err, reputation.ErrInsufficientBalance) {
				t.Fatalf("err = %v", err)
			}
			if name == "reserved below zero" && !errors.Is(err, reputation.ErrInsufficientReserve) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestAdjustReputationWalletLeavesScore(t *testing.T) {
	repo, db, cleanup := newRepTestStore(t)
	defer cleanup()

	settle(t, repo, reputation.SocialRequest{
		ChatID: 100, ActorID: 1, TargetID: 7, Kind: reputation.KindPraise,
		Source: reputation.SourceSlash, SourceMessageID: 1,
	})
	if err := db.Update(func(tx *bolt.Tx) error {
		return storage.AdjustReputationWallet(tx, 100, 7, -5, 5)
	}); err != nil {
		t.Fatal(err)
	}
	account := mustWallet(t, repo, 100, 7)
	if account.Available != 8 || account.Reserved != 5 || account.Score != 3 {
		t.Fatalf("wallet adjustment changed the title score: %+v", account)
	}
	if title := reputation.TitleForScore(account.Score); title.Name != "Участник" {
		t.Fatalf("title after wallet-only change = %q", title.Name)
	}
}

func TestReputationLeaderboardByTitleScore(t *testing.T) {
	repo, db, cleanup := newRepTestStore(t)
	defer cleanup()

	// Actor 1 can make three outgoing settlements per day. Three days
	// produce the scores below; an extra wallet-only row must not appear.
	day := func(n int) time.Time {
		return time.Date(2026, 11, 18+n, 12, 0, 0, 0, time.UTC)
	}
	for i := range 2 {
		settle(t, repo, reputation.SocialRequest{
			ChatID: 100, ActorID: 1, TargetID: 100, Kind: reputation.KindPraise,
			Source: reputation.SourceSlash, SourceMessageID: 10 + i, Now: day(i),
		})
	}
	settle(t, repo, reputation.SocialRequest{
		ChatID: 100, ActorID: 1, TargetID: 101, Kind: reputation.KindPraise,
		Source: reputation.SourceSlash, SourceMessageID: 20, Now: day(0),
	})
	settle(t, repo, reputation.SocialRequest{
		ChatID: 100, ActorID: 1, TargetID: 102, Kind: reputation.KindPraise,
		Source: reputation.SourceSlash, SourceMessageID: 21, Now: day(2),
	})
	if err := db.Update(func(tx *bolt.Tx) error {
		return storage.AdjustReputationWallet(tx, 100, 103, 5, 0)
	}); err != nil {
		t.Fatal(err)
	}

	entries, err := repo.Leaderboard(context.Background(), -100, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []reputation.Entry{
		{UserID: 100, Score: 6},
		{UserID: 101, Score: 3},
		{UserID: 102, Score: 3},
	}
	if len(entries) != len(want) {
		t.Fatalf("entries = %+v, want %+v", entries, want)
	}
	for i := range want {
		if entries[i] != want[i] {
			t.Fatalf("entries = %+v, want %+v", entries, want)
		}
	}

	limited, err := repo.Leaderboard(context.Background(), 100, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 || limited[0].UserID != 100 {
		t.Fatalf("limited = %+v", limited)
	}
}

func TestReputationLegacyMigrationExactlyOnce(t *testing.T) {
	repo, db, cleanup := newRepTestStore(t)
	defer cleanup()
	const chatID, userID int64 = 100, 1
	putLegacyBalance(t, db, chatID, userID, 42)

	needed, err := storage.ReputationMigrationNeeded(db)
	if err != nil {
		t.Fatal(err)
	}
	if !needed {
		t.Fatal("legacy balance must be reported as needing migration")
	}

	var backup bytes.Buffer
	migrated, err := storage.MigrateReputation(db, &backup)
	if err != nil {
		t.Fatal(err)
	}
	if migrated != 1 {
		t.Fatalf("migrated = %d, want 1", migrated)
	}
	if backup.Len() == 0 {
		t.Fatal("migration must write a backup snapshot when asked")
	}

	account := mustWallet(t, repo, chatID, userID)
	if account.Available != 42 || account.Score != 42 || account.Reserved != 0 {
		t.Fatalf("migrated account = %+v, want available 42 score 42", account)
	}
	// An absent legacy record takes the new-member defaults.
	absent := mustWallet(t, repo, chatID, 2)
	if absent.Available != 10 || absent.Score != 0 {
		t.Fatalf("absent legacy defaults = %+v", absent)
	}

	migrated, err = storage.MigrateReputation(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if migrated != 0 {
		t.Fatalf("second migration rewrote %d rows", migrated)
	}
	again := mustWallet(t, repo, chatID, userID)
	if again != account {
		t.Fatalf("second migration doubled the wallet: %+v -> %+v", account, again)
	}
	needed, err = storage.ReputationMigrationNeeded(db)
	if err != nil {
		t.Fatal(err)
	}
	if needed {
		t.Fatal("marker must clear the migration-needed probe")
	}

	if err := db.View(func(tx *bolt.Tx) error {
		bkt := tx.Bucket([]byte("reputation"))
		if got := string(bkt.Get([]byte("meta:v"))); got != "2" {
			t.Fatalf("version marker = %q, want 2", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReputationLazyMigrationOnFirstSettlement(t *testing.T) {
	repo, db, cleanup := newRepTestStore(t)
	defer cleanup()
	const chatID, target int64 = 100, 2
	putLegacyBalance(t, db, chatID, target, 42)

	result := settle(t, repo, reputation.SocialRequest{
		ChatID: chatID, ActorID: 1, TargetID: target, Kind: reputation.KindPraise,
		Source: reputation.SourceSlash, SourceMessageID: 1,
	})
	if result.Target.Available != 45 || result.Target.Score != 45 {
		t.Fatalf("lazily migrated target = %+v, want 45/45", result.Target)
	}
	needed, err := storage.ReputationMigrationNeeded(db)
	if err != nil {
		t.Fatal(err)
	}
	if needed {
		t.Fatal("settlement must commit the migration marker")
	}
}

func TestMigrateReputationChatID(t *testing.T) {
	repo, db, cleanup := newRepTestStore(t)
	defer cleanup()

	putLegacyBalance(t, db, 100, 1, 7)
	settle(t, repo, reputation.SocialRequest{
		ChatID: 100, ActorID: 2, TargetID: 3, Kind: reputation.KindPraise,
		Source: reputation.SourceSlash, SourceMessageID: 1,
		Now: time.Date(2026, 11, 18, 1, 0, 0, 0, time.UTC),
	})

	moved, err := storage.MigrateReputationChatID(context.Background(), db, 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	if moved == 0 {
		t.Fatal("expected rekeyed reputation rows")
	}
	if got := mustWallet(t, repo, 200, 1); got.Available != 7 || got.Score != 7 {
		t.Fatalf("moved legacy wallet = %+v", got)
	}
	if got := mustWallet(t, repo, 200, 3); got.Score != 3 {
		t.Fatalf("moved new wallet = %+v", got)
	}
	if got := mustWallet(t, repo, 100, 1); got.Available != 10 || got.Score != 0 {
		t.Fatalf("old chat still has a wallet: %+v", got)
	}

	// The pair and sender counters moved with the chat: the reverse
	// direction of the already settled pair is still blocked.
	_, err = repo.Settle(context.Background(), reputation.SocialRequest{
		ChatID: 200, ActorID: 3, TargetID: 2, Kind: reputation.KindPraise,
		Source: reputation.SourceSlash, SourceMessageID: 90,
		Now: time.Date(2026, 11, 18, 13, 0, 0, 0, time.UTC),
	})
	if !errors.Is(err, reputation.ErrPairLimit) {
		t.Fatalf("pair marker did not move: %v", err)
	}
	// Actor 2 already has one outgoing settlement from before the move;
	// two more fit and the fourth is blocked by the sender limit.
	for i, target := range []int64{4, 5} {
		settle(t, repo, reputation.SocialRequest{
			ChatID: 200, ActorID: 2, TargetID: target, Kind: reputation.KindPraise,
			Source: reputation.SourceSlash, SourceMessageID: 100 + i,
			Now: time.Date(2026, 11, 18, 14, 0, 0, 0, time.UTC),
		})
	}
	_, err = repo.Settle(context.Background(), reputation.SocialRequest{
		ChatID: 200, ActorID: 2, TargetID: 6, Kind: reputation.KindPraise,
		Source: reputation.SourceSlash, SourceMessageID: 102,
		Now: time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC),
	})
	if !errors.Is(err, reputation.ErrSenderLimit) {
		t.Fatalf("sender marker did not move: %v", err)
	}

	// The destination row wins instead of being summed.
	putLegacyBalance(t, db, 100, 9, 5)
	putLegacyBalance(t, db, 200, 9, 11)
	if _, err := storage.MigrateReputationChatID(context.Background(), db, 100, 200); err != nil {
		t.Fatal(err)
	}
	if got := mustWallet(t, repo, 200, 9); got.Available != 11 {
		t.Fatalf("destination wallet must win, got %+v", got)
	}
}
