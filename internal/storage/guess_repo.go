package storage

import (
	"bytes"
	"context"
	"encoding/json"
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
	db *bolt.DB
}

func NewGuessRepo(db *bolt.DB) *GuessRepo {
	return &GuessRepo{db: db}
}

// guessRoundKey is the per-chat round key. Unexported: defined here
// rather than in keys.go so the games work can land without editing the
// shared storage wiring.
func guessRoundKey(absChatID int64) []byte {
	return []byte(fmt.Sprintf("gr:%020d", absChatID))
}

func guessWinKey(absChatID, userID int64) []byte {
	return []byte(fmt.Sprintf("gw:%020d:%020d", absChatID, userID))
}

func guessWinChatPrefix(absChatID int64) []byte {
	return []byte(fmt.Sprintf("gw:%020d:", absChatID))
}

// GetRound returns the chat's round or guess.ErrNotFound.
func (r *GuessRepo) GetRound(_ context.Context, absChatID int64) (*guess.Round, error) {
	var rec guess.Round
	err := r.db.View(func(tx *bolt.Tx) error {
		bkt := tx.Bucket(bktGuessRound)
		if bkt == nil {
			// Bucket not created yet (no write has happened) -> no round.
			return guess.ErrNotFound
		}
		data := bkt.Get(guessRoundKey(absChatID))
		if data == nil {
			return guess.ErrNotFound
		}
		return json.Unmarshal(data, &rec)
	})
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// PutRound writes the round unconditionally.
func (r *GuessRepo) PutRound(_ context.Context, rec guess.Round) error {
	if rec.AbsChatID == 0 {
		return fmt.Errorf("guess repo: zero AbsChatID")
	}
	data, err := json.Marshal(&rec)
	if err != nil {
		return err
	}
	return r.db.Update(func(tx *bolt.Tx) error {
		bkt, err := tx.CreateBucketIfNotExists(bktGuessRound)
		if err != nil {
			return err
		}
		return bkt.Put(guessRoundKey(rec.AbsChatID), data)
	})
}

// DeleteRound removes the chat's round. Missing round is a no-op.
func (r *GuessRepo) DeleteRound(_ context.Context, absChatID int64) error {
	if absChatID == 0 {
		return fmt.Errorf("guess repo: zero AbsChatID")
	}
	return r.db.Update(func(tx *bolt.Tx) error {
		bkt := tx.Bucket(bktGuessRound)
		if bkt == nil {
			return nil
		}
		return bkt.Delete(guessRoundKey(absChatID))
	})
}

// IncrementWin creates the entry on first call and bumps Wins after.
// Username/FirstName refresh on every call so renames propagate (same
// pattern as QuizRepo.IncrementCorrect).
func (r *GuessRepo) IncrementWin(_ context.Context, e guess.WinEntry) error {
	if e.AbsChatID == 0 || e.UserID == 0 {
		return fmt.Errorf("guess repo: zero AbsChatID or UserID")
	}
	return r.db.Update(func(tx *bolt.Tx) error {
		bkt, err := tx.CreateBucketIfNotExists(bktGuessWins)
		if err != nil {
			return err
		}
		key := guessWinKey(e.AbsChatID, e.UserID)

		var existing guess.WinEntry
		if data := bkt.Get(key); data != nil {
			if err := json.Unmarshal(data, &existing); err != nil {
				return err
			}
		} else {
			existing = guess.WinEntry{AbsChatID: e.AbsChatID, UserID: e.UserID}
		}
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
		data, err := json.Marshal(&existing)
		if err != nil {
			return err
		}
		return bkt.Put(key, data)
	})
}

// TopWins returns up to limit entries for the chat, Wins desc, ties
// broken by earlier LastWonAt. limit<=0 returns all.
func (r *GuessRepo) TopWins(_ context.Context, absChatID int64, limit int) ([]guess.WinEntry, error) {
	var all []guess.WinEntry
	err := r.db.View(func(tx *bolt.Tx) error {
		bkt := tx.Bucket(bktGuessWins)
		if bkt == nil {
			return nil
		}
		c := bkt.Cursor()
		prefix := guessWinChatPrefix(absChatID)
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			var e guess.WinEntry
			if err := json.Unmarshal(v, &e); err != nil {
				continue
			}
			all = append(all, e)
		}
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
