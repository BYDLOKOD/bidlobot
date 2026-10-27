package storage

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

var bktBirthdayGreeted = []byte("birthday_greeted")

// BirthdayRepo stores a short-lived "already greeted" marker per
// (chat, day, user). It deliberately stores no birth date: the date is
// read from the Telegram profile and discarded after each check.
type BirthdayRepo struct {
	db *bolt.DB
}

func NewBirthdayRepo(db *bolt.DB) *BirthdayRepo {
	return &BirthdayRepo{db: db}
}

// MarkGreeted records a successful greeting attempt. The value is the
// Unix timestamp used by Prune; the key carries the day.
func (r *BirthdayRepo) MarkGreeted(_ context.Context, absChatID, userID int64, day string, at time.Time) error {
	return r.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bktBirthdayGreeted)
		if b == nil {
			return fmt.Errorf("storage: bucket %s missing", bktBirthdayGreeted)
		}
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], uint64(at.UTC().Unix()))
		return b.Put(BirthdayGreetedKey(absChatID, userID, day), buf[:])
	})
}

func (r *BirthdayRepo) IsGreeted(_ context.Context, absChatID, userID int64, day string) (bool, error) {
	var ok bool
	err := r.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bktBirthdayGreeted)
		if b == nil {
			return nil
		}
		ok = b.Get(BirthdayGreetedKey(absChatID, userID, day)) != nil
		return nil
	})
	return ok, err
}

func (r *BirthdayRepo) ClearGreeted(_ context.Context, absChatID, userID int64, day string) error {
	return r.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bktBirthdayGreeted)
		if b == nil {
			return nil
		}
		return b.Delete(BirthdayGreetedKey(absChatID, userID, day))
	})
}

// Prune deletes markers for one chat whose stored timestamp is before
// the cutoff. Malformed values are removed as well, so a partial write
// cannot keep a stale marker forever.
func (r *BirthdayRepo) Prune(_ context.Context, absChatID int64, before time.Time) error {
	return r.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bktBirthdayGreeted)
		if b == nil {
			return nil
		}
		prefix := BirthdayGreetedChatPrefix(absChatID)
		cutoff := uint64(before.UTC().Unix())

		var doomed [][]byte
		c := b.Cursor()
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			if len(v) < 8 || binary.BigEndian.Uint64(v) < cutoff {
				doomed = append(doomed, append([]byte(nil), k...))
			}
		}
		for _, k := range doomed {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}
