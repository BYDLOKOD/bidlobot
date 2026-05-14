package storage

import (
	"bytes"
	"context"
	"encoding/json"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/domain/moderation"
)

var (
	bktWarnings     = []byte("warnings")
	bktWarnsByTarget = []byte("warns_by_target")
)

type WarnRepo struct {
	db *bolt.DB
}

func NewWarnRepo(db *bolt.DB) *WarnRepo {
	return &WarnRepo{db: db}
}

func (r *WarnRepo) CreateWarning(_ context.Context, w *moderation.Warning) (int, error) {
	var activeCount int

	err := r.db.Update(func(tx *bolt.Tx) error {
		warnBkt := tx.Bucket(bktWarnings)
		idxBkt := tx.Bucket(bktWarnsByTarget)

		count := 0
		prefix := WarnTargetPrefix(w.ChatID, w.TargetUserID)
		c := idxBkt.Cursor()
		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			uuid := extractUUIDFromIndex(k, prefix)
			data := warnBkt.Get(WarnKey(uuid))
			if data == nil {
				continue
			}
			var existing moderation.Warning
			if err := json.Unmarshal(data, &existing); err != nil {
				continue
			}
			if existing.Active {
				count++
			}
		}

		w.Active = true
		data, err := json.Marshal(w)
		if err != nil {
			return err
		}
		if err := warnBkt.Put(WarnKey(w.ID), data); err != nil {
			return err
		}
		if err := idxBkt.Put(WarnTargetIndex(w.ChatID, w.TargetUserID, w.ID), nil); err != nil {
			return err
		}

		activeCount = count + 1
		return nil
	})

	return activeCount, err
}

func (r *WarnRepo) ListActive(_ context.Context, targetUserID, absChatID int64) ([]moderation.Warning, error) {
	var results []moderation.Warning
	err := r.db.View(func(tx *bolt.Tx) error {
		warnBkt := tx.Bucket(bktWarnings)
		idxBkt := tx.Bucket(bktWarnsByTarget)
		prefix := WarnTargetPrefix(absChatID, targetUserID)
		c := idxBkt.Cursor()

		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			uuid := extractUUIDFromIndex(k, prefix)
			data := warnBkt.Get(WarnKey(uuid))
			if data == nil {
				continue
			}
			var w moderation.Warning
			if err := json.Unmarshal(data, &w); err != nil {
				continue
			}
			if w.Active {
				results = append(results, w)
			}
		}
		return nil
	})
	return results, err
}

func (r *WarnRepo) CountActive(_ context.Context, targetUserID, absChatID int64) (int, error) {
	var count int
	err := r.db.View(func(tx *bolt.Tx) error {
		warnBkt := tx.Bucket(bktWarnings)
		idxBkt := tx.Bucket(bktWarnsByTarget)
		prefix := WarnTargetPrefix(absChatID, targetUserID)
		c := idxBkt.Cursor()

		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			uuid := extractUUIDFromIndex(k, prefix)
			data := warnBkt.Get(WarnKey(uuid))
			if data == nil {
				continue
			}
			var w moderation.Warning
			if err := json.Unmarshal(data, &w); err != nil {
				continue
			}
			if w.Active {
				count++
			}
		}
		return nil
	})
	return count, err
}

func (r *WarnRepo) ClearWarnings(_ context.Context, targetUserID, absChatID int64) error {
	return r.db.Update(func(tx *bolt.Tx) error {
		warnBkt := tx.Bucket(bktWarnings)
		idxBkt := tx.Bucket(bktWarnsByTarget)
		prefix := WarnTargetPrefix(absChatID, targetUserID)
		c := idxBkt.Cursor()

		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			uuid := extractUUIDFromIndex(k, prefix)
			key := WarnKey(uuid)
			data := warnBkt.Get(key)
			if data == nil {
				continue
			}
			var w moderation.Warning
			if err := json.Unmarshal(data, &w); err != nil {
				continue
			}
			if !w.Active {
				continue
			}
			w.Active = false
			updated, err := json.Marshal(&w)
			if err != nil {
				continue
			}
			if err := warnBkt.Put(key, updated); err != nil {
				return err
			}
		}
		return nil
	})
}

func extractUUIDFromIndex(key, prefix []byte) string {
	return string(key[len(prefix):])
}
