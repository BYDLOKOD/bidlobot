package storage

import (
	"context"
	"fmt"
	"sort"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/games/guess"
)

// Bucket names owned by the guess game. They are created on first write
// via CreateBucketIfNotExists so the repo works even before bolt.go
// registers them (and stays correct after it does). For durability the
// names SHOULD also be added to the `buckets` slice in bolt.go - see the
// wiring report.
var (
	bktGuessRound = []byte("guess_round")
	bktGuessWins  = []byte("guess_wins")
)

// GuessRepo persists per-chat round state and the per-chat win
// leaderboard. Implements guess.Store. Schema:
//
//	gr:<absChatID:020d>                      -> JSON{Round}
//	gw:<absChatID:020d>:<userID:020d>        -> JSON{WinEntry}
//
// Top-N is sorted in-app; a 200-member chat produces at most a few
// thousand win rows, well below where a secondary index would pay off
// (same reasoning as QuizRepo).
type GuessRepo struct {
	rounds *jsonBucket[guess.Round]
	wins   *jsonBucket[guess.WinEntry]
}

func NewGuessRepo(db *bolt.DB) *GuessRepo {
	return &GuessRepo{
		rounds: newJSONBucket[guess.Round](db, bktGuessRound),
		wins:   newJSONBucket[guess.WinEntry](db, bktGuessWins),
	}
}

// guessRoundKey is the per-chat round key. Unexported: defined here
// rather than in keys.go so the games work can land without editing the
// shared storage wiring.
func guessRoundKey(absChatID int64) []byte {
	return keyf("gr:%020d", absChatID)
}

func guessWinKey(absChatID, userID int64) []byte {
	return keyf("gw:%020d:%020d", absChatID, userID)
}

func guessWinChatPrefix(absChatID int64) []byte {
	return keyf("gw:%020d:", absChatID)
}

// GetRound returns the chat's round or guess.ErrNotFound.
func (r *GuessRepo) GetRound(_ context.Context, absChatID int64) (*guess.Round, error) {
	return r.rounds.Get(guessRoundKey(absChatID), guess.ErrNotFound)
}

// PutRound writes the round unconditionally.
func (r *GuessRepo) PutRound(_ context.Context, rec guess.Round) error {
	if rec.AbsChatID == 0 {
		return fmt.Errorf("guess repo: zero AbsChatID")
	}
	return r.rounds.Put(guessRoundKey(rec.AbsChatID), rec)
}

// DeleteRound removes the chat's round. Missing round is a no-op.
func (r *GuessRepo) DeleteRound(_ context.Context, absChatID int64) error {
	if absChatID == 0 {
		return fmt.Errorf("guess repo: zero AbsChatID")
	}
	return r.rounds.Delete(guessRoundKey(absChatID))
}

// IncrementWin creates the entry on first call and bumps Wins after.
// Username/FirstName refresh on every call so renames propagate (same
// pattern as QuizRepo.IncrementCorrect).
func (r *GuessRepo) IncrementWin(_ context.Context, e guess.WinEntry) error {
	if e.AbsChatID == 0 || e.UserID == 0 {
		return fmt.Errorf("guess repo: zero AbsChatID or UserID")
	}
	return r.wins.Update(guessWinKey(e.AbsChatID, e.UserID), func(existing *guess.WinEntry) error {
		existing.AbsChatID = e.AbsChatID
		existing.UserID = e.UserID
		existing.Wins++
		if e.Username != "" {
			existing.Username = e.Username
		}
		if e.FirstName != "" {
			existing.FirstName = e.FirstName
		}
		if !e.LastWonAt.IsZero() {
			existing.LastWonAt = e.LastWonAt.UTC()
		}
		return nil
	})
}

// TopWins returns up to limit entries for the chat, Wins desc, ties
// broken by earlier LastWonAt. limit<=0 returns all.
func (r *GuessRepo) TopWins(_ context.Context, absChatID int64, limit int) ([]guess.WinEntry, error) {
	var all []guess.WinEntry
	err := r.wins.ScanPrefix(guessWinChatPrefix(absChatID), func(_ []byte, e guess.WinEntry) error {
		all = append(all, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Wins != all[j].Wins {
			return all[i].Wins > all[j].Wins
		}
		return all[i].LastWonAt.Before(all[j].LastWonAt)
	})
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}
