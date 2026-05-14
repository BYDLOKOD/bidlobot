package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/domain/pending"
)

var bktPending = []byte("pending_actions")

type PendingRepo struct {
	db *bolt.DB
}

func NewPendingRepo(db *bolt.DB) *PendingRepo {
	return &PendingRepo{db: db}
}

// NewID returns a 16-char hex string (8 random bytes) suitable for
// embedding into callback_data. Callable from outside without holding
// the DB so callers can prepare the Action then write once.
func NewID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("random id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func PendingKey(id string) []byte {
	return []byte(fmt.Sprintf("pa:%s", id))
}

func (r *PendingRepo) Create(_ context.Context, a pending.Action) error {
	if a.ID == "" {
		return fmt.Errorf("pending: empty ID")
	}
	if a.ExpiresAt.IsZero() {
		return fmt.Errorf("pending: zero ExpiresAt")
	}
	return r.db.Update(func(tx *bolt.Tx) error {
		bkt := tx.Bucket(bktPending)
		if existing := bkt.Get(PendingKey(a.ID)); existing != nil {
			return fmt.Errorf("pending: ID collision %q", a.ID)
		}
		data, err := json.Marshal(&a)
		if err != nil {
			return err
		}
		return bkt.Put(PendingKey(a.ID), data)
	})
}

// Get returns the pending action by ID. Expired entries are removed and
// reported as ErrExpired. Missing entries return ErrNotFound.
func (r *PendingRepo) Get(_ context.Context, id string) (*pending.Action, error) {
	var found pending.Action
	var expired bool
	err := r.db.Update(func(tx *bolt.Tx) error {
		bkt := tx.Bucket(bktPending)
		key := PendingKey(id)
		data := bkt.Get(key)
		if data == nil {
			return pending.ErrNotFound
		}
		if err := json.Unmarshal(data, &found); err != nil {
			return err
		}
		if time.Now().UTC().After(found.ExpiresAt) {
			expired = true
			return bkt.Delete(key)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if expired {
		return nil, pending.ErrExpired
	}
	return &found, nil
}

func (r *PendingRepo) Delete(_ context.Context, id string) error {
	return r.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bktPending).Delete(PendingKey(id))
	})
}

// PinChatID rewrites the stored Action's AbsChatID. Called by the
// callback dispatcher on the first observed callback so that any later
// callback in a different chat (e.g. a forwarded inline message) can
// be rejected. Idempotent if the existing AbsChatID equals the new one.
func (r *PendingRepo) PinChatID(_ context.Context, id string, absChatID int64) error {
	if id == "" || absChatID == 0 {
		return fmt.Errorf("pending: PinChatID requires non-empty id and chat")
	}
	return r.db.Update(func(tx *bolt.Tx) error {
		bkt := tx.Bucket(bktPending)
		key := PendingKey(id)
		data := bkt.Get(key)
		if data == nil {
			return pending.ErrNotFound
		}
		var a pending.Action
		if err := json.Unmarshal(data, &a); err != nil {
			return err
		}
		if a.AbsChatID == absChatID {
			return nil
		}
		a.AbsChatID = absChatID
		updated, err := json.Marshal(&a)
		if err != nil {
			return err
		}
		return bkt.Put(key, updated)
	})
}

func (r *PendingRepo) GarbageCollect(_ context.Context, now time.Time) (int, error) {
	now = now.UTC()
	removed := 0
	err := r.db.Update(func(tx *bolt.Tx) error {
		bkt := tx.Bucket(bktPending)
		c := bkt.Cursor()
		var toDelete [][]byte
		prefix := []byte("pa:")
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			var a pending.Action
			if err := json.Unmarshal(v, &a); err != nil {
				toDelete = append(toDelete, append([]byte(nil), k...))
				continue
			}
			if now.After(a.ExpiresAt) {
				toDelete = append(toDelete, append([]byte(nil), k...))
			}
		}
		for _, k := range toDelete {
			if err := bkt.Delete(k); err != nil {
				return err
			}
			removed++
		}
		return nil
	})
	return removed, err
}
