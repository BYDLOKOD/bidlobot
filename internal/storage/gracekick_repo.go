package storage

import (
	"bytes"
	"context"
	"encoding/json"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/domain/gracekick"
)

var bktGraceKick = []byte("gracekick")

// GraceKickRepo persists open grace tickets under bucket "gracekick",
// keyed by GraceKickKey. The chat-leading key lets the daily sweep scan
// one chat's tickets with a single prefix cursor. Put is an idempotent
// upsert, so a re-tag of the same member just refreshes the deadline.
type GraceKickRepo struct {
	db *bolt.DB
}

func NewGraceKickRepo(db *bolt.DB) *GraceKickRepo { return &GraceKickRepo{db: db} }

func (r *GraceKickRepo) Put(_ context.Context, rec gracekick.Record) error {
	rec.TaggedAt = rec.TaggedAt.UTC()
	rec.GraceDeadline = rec.GraceDeadline.UTC()
	data, err := json.Marshal(&rec)
	if err != nil {
		return err
	}
	return r.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bktGraceKick).Put(GraceKickKey(rec.AbsChatID, rec.UserID), data)
	})
}

func (r *GraceKickRepo) ListByChat(_ context.Context, absChatID int64) ([]gracekick.Record, error) {
	prefix := GraceKickChatPrefix(absChatID)
	var out []gracekick.Record
	err := r.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bktGraceKick).Cursor()
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			var rec gracekick.Record
			if uerr := json.Unmarshal(v, &rec); uerr != nil {
				return uerr
			}
			out = append(out, rec)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r *GraceKickRepo) Delete(_ context.Context, absChatID, userID int64) error {
	return r.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bktGraceKick).Delete(GraceKickKey(absChatID, userID))
	})
}
