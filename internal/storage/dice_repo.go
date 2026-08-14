package storage

import (
	"context"
	"fmt"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/games/dice"
)

var bktDiceLeaderboard = []byte("dice_leaderboard")

// DiceRepo persists per-chat-per-emoji top scores. Implements
// dice.Store; constructed against the same *bolt.DB as the other
// repos so a single transactional surface stays available.
type DiceRepo struct {
	records *jsonBucket[dice.Record]
}

func NewDiceRepo(db *bolt.DB) *DiceRepo {
	return &DiceRepo{records: newJSONBucket[dice.Record](db, bktDiceLeaderboard)}
}

// DiceKey returns the bbolt key for (absChatID, emoji). Emoji is
// included verbatim (not hex-encoded) - it is bounded to a tiny
// allowed set so collisions and binary safety are not concerns.
func DiceKey(absChatID int64, emoji string) []byte {
	return keyf("dl:%020d:%s", absChatID, emoji)
}

// Get returns the existing record or dice.ErrNotFound when no roll has
// been recorded yet for this (chat, emoji) pair.
func (r *DiceRepo) Get(_ context.Context, absChatID int64, emoji string) (*dice.Record, error) {
	return r.records.Get(DiceKey(absChatID, emoji), dice.ErrNotFound)
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
	return r.records.Put(DiceKey(rec.AbsChatID, rec.Emoji), rec)
}
