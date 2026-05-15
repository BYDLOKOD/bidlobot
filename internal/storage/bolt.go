package storage

import (
	"context"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// "profiles" and "profiles_by_chat" are intentionally kept as empty buckets
// so that data from the archived bio domain (branch archive/profiles-bio,
// tag v0-bio-archive) can be restored without bbolt schema changes if the
// feature returns.
var buckets = []string{
	"profiles",
	"profiles_by_chat",
	"stats",
	"stats_by_chat",
	"warnings",
	"warns_by_target",
	"members",
	"members_by_chat",
	"chats",
	"pending_actions",
	"dice_leaderboard",
	"quiz_leaderboard",
	"dm_sessions",
}

type BoltStore struct {
	db *bolt.DB
}

func NewBoltStore(path string) (*BoltStore, error) {
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open bolt db: %w", err)
	}

	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range buckets {
			if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
				return fmt.Errorf("create bucket %s: %w", name, err)
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("init buckets: %w", err)
	}

	return &BoltStore{db: db}, nil
}

func (s *BoltStore) DB() *bolt.DB {
	return s.db
}

func (s *BoltStore) Close() error {
	return s.db.Close()
}

// MigrateChatID adapts the package-level [MigrateChatID] to the
// tgclient.Migrator interface so the wrapper can be wired against a
// BoltStore without an extra adapter type. The report is logged at
// info level via the package-level helper rather than returned, since
// the wrapper interface only needs error semantics.
func (s *BoltStore) MigrateChatID(ctx context.Context, oldAbs, newAbs int64) error {
	_, err := MigrateChatID(ctx, s.db, oldAbs, newAbs)
	return err
}
