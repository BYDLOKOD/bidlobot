package storage

import (
	"bytes"
	"context"
	"encoding/json"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/domain/stats"
)

var (
	bktStats       = []byte("stats")
	bktStatsByChat = []byte("stats_by_chat")
)

type StatsRepo struct {
	db *bolt.DB
}

func NewStatsRepo(db *bolt.DB) *StatsRepo {
	return &StatsRepo{db: db}
}

func (r *StatsRepo) Get(_ context.Context, userID, absChatID int64) (*stats.Stats, error) {
	var s stats.Stats
	err := r.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(bktStats).Get(StatsKey(userID, absChatID))
		if data == nil {
			return stats.ErrNotFound
		}
		return json.Unmarshal(data, &s)
	})
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *StatsRepo) ListByChat(_ context.Context, absChatID int64) ([]stats.Stats, error) {
	var results []stats.Stats
	err := r.db.View(func(tx *bolt.Tx) error {
		statsBkt := tx.Bucket(bktStats)
		idx := tx.Bucket(bktStatsByChat)
		prefix := StatsChatPrefix(absChatID)
		c := idx.Cursor()

		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			parts := bytes.SplitN(k, []byte(":"), 3)
			if len(parts) < 3 {
				continue
			}
			data := statsBkt.Get(StatsKey(parseID(parts[2]), parseID(parts[1])))
			if data == nil {
				continue
			}
			var s stats.Stats
			if err := json.Unmarshal(data, &s); err != nil {
				continue
			}
			results = append(results, s)
		}
		return nil
	})
	return results, err
}

func (r *StatsRepo) Flush(_ context.Context, batch map[stats.FlushKey]*stats.FlushDelta) error {
	if len(batch) == 0 {
		return nil
	}
	return r.db.Update(func(tx *bolt.Tx) error {
		bkt := tx.Bucket(bktStats)
		idx := tx.Bucket(bktStatsByChat)

		for key, delta := range batch {
			dbKey := StatsKey(key.UserID, key.AbsChatID)
			var s stats.Stats

			if existing := bkt.Get(dbKey); existing != nil {
				if err := json.Unmarshal(existing, &s); err != nil {
					return err
				}
				s.MessageCount += delta.CountDelta
				if delta.LastSeen.After(s.LastSeen) {
					s.LastSeen = delta.LastSeen
				}
			} else {
				s = stats.Stats{
					UserID:       key.UserID,
					ChatID:       key.AbsChatID,
					MessageCount: delta.CountDelta,
					FirstSeen:    delta.FirstSeen,
					LastSeen:     delta.LastSeen,
				}
				if err := idx.Put(StatsChatIndex(key.AbsChatID, key.UserID), nil); err != nil {
					return err
				}
			}

			data, err := json.Marshal(&s)
			if err != nil {
				return err
			}
			if err := bkt.Put(dbKey, data); err != nil {
				return err
			}
		}
		return nil
	})
}
