package storage

import (
	"context"
	"encoding/json"
	"fmt"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/games/hangman"
)

// bktHangmanRound is the bucket for per-chat hangman round state.
// Created on first write via CreateBucketIfNotExists so the repo works
// before bolt.go registers it; the name SHOULD also be added to the
// `buckets` slice in bolt.go for durability - see the wiring report.
var bktHangmanRound = []byte("hangman_round")

// HangmanRepo persists per-chat round state. Implements hangman.Store.
// Schema:
//
//	hr:<absChatID:020d>  -> JSON{Round}
//
// Round.Used is a map[string]bool; encoding/json handles it natively
// (string keys), so no custom (de)serialization is needed.
type HangmanRepo struct {
	db *bolt.DB
}

func NewHangmanRepo(db *bolt.DB) *HangmanRepo {
	return &HangmanRepo{db: db}
}

// hangmanRoundKey is the per-chat round key. Unexported and local so the
// games work lands without editing shared storage wiring.
func hangmanRoundKey(absChatID int64) []byte {
	return []byte(fmt.Sprintf("hr:%020d", absChatID))
}

// GetRound returns the chat's round or hangman.ErrNotFound. A nil Used
// map is normalized to an empty map so callers never deref nil.
func (r *HangmanRepo) GetRound(_ context.Context, absChatID int64) (*hangman.Round, error) {
	var rec hangman.Round
	err := r.db.View(func(tx *bolt.Tx) error {
		bkt := tx.Bucket(bktHangmanRound)
		if bkt == nil {
			return hangman.ErrNotFound
		}
		data := bkt.Get(hangmanRoundKey(absChatID))
		if data == nil {
			return hangman.ErrNotFound
		}
		return json.Unmarshal(data, &rec)
	})
	if err != nil {
		return nil, err
	}
	if rec.Used == nil {
		rec.Used = make(map[string]bool)
	}
	return &rec, nil
}

// PutRound writes the round unconditionally.
func (r *HangmanRepo) PutRound(_ context.Context, rec hangman.Round) error {
	if rec.AbsChatID == 0 {
		return fmt.Errorf("hangman repo: zero AbsChatID")
	}
	if rec.Word == "" {
		return fmt.Errorf("hangman repo: empty word")
	}
	data, err := json.Marshal(&rec)
	if err != nil {
		return err
	}
	return r.db.Update(func(tx *bolt.Tx) error {
		bkt, err := tx.CreateBucketIfNotExists(bktHangmanRound)
		if err != nil {
			return err
		}
		return bkt.Put(hangmanRoundKey(rec.AbsChatID), data)
	})
}

// DeleteRound removes the chat's round. Missing round is a no-op.
func (r *HangmanRepo) DeleteRound(_ context.Context, absChatID int64) error {
	if absChatID == 0 {
		return fmt.Errorf("hangman repo: zero AbsChatID")
	}
	return r.db.Update(func(tx *bolt.Tx) error {
		bkt := tx.Bucket(bktHangmanRound)
		if bkt == nil {
			return nil
		}
		return bkt.Delete(hangmanRoundKey(absChatID))
	})
}
