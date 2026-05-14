package storage

import (
	"context"
	"encoding/json"
	"fmt"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/games/dice"
)

var bktDiceLeaderboard = []byte("dice_leaderboard")

// DiceRepo persists per-chat-per-emoji top scores. Implements
// dice.Store; constructed against the same *bolt.DB as the other
// repos so a single transactional surface stays available.
type DiceRepo struct {
	db *bolt.DB
}

func NewDiceRepo(db *bolt.DB) *DiceRepo {
	return &DiceRepo{db: db}
}

// DiceKey returns the bbolt key for (absChatID, emoji). Emoji is
// included verbatim (not hex-encoded) - it is bounded to a tiny
// allowed set so collisions and binary safety are not concerns.
func DiceKey(absChatID int64, emoji string) []byte {
	return []byte(fmt.Sprintf("dl:%020d:%s", absChatID, emoji))
}

// Get returns the existing record or dice.ErrNotFound when no roll has
// been recorded yet for this (chat, emoji) pair.
func (r *DiceRepo) Get(_ context.Context, absChatID int64, emoji string) (*dice.Record, error) {
	var rec dice.Record
	err := r.db.View(func(tx *bolt.Tx) error {
		bkt := tx.Bucket(bktDiceLeaderboard)
		data := bkt.Get(DiceKey(absChatID, emoji))
		if data == nil {
			return dice.ErrNotFound
		}
		return json.Unmarshal(data, &rec)
	})
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// Put writes the record unconditionally. The dice service is responsible
// for comparison logic; the repo only persists.
func (r *DiceRepo) Put(_ context.Context, rec dice.Record) error {
	if rec.AbsChatID == 0 {
		return fmt.Errorf("dice repo: zero AbsChatID")
	}
	if rec.Emoji == "" {
		return fmt.Errorf("dice repo: empty emoji")
	}
	data, err := json.Marshal(&rec)
	if err != nil {
		return err
	}
	return r.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bktDiceLeaderboard).Put(DiceKey(rec.AbsChatID, rec.Emoji), data)
	})
}
