package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Buckets from the archived bio domain ("profiles", "profiles_by_chat")
// are no longer created; existing databases keep them untouched on
// disk (bbolt never drops buckets implicitly), and migrate.go skips
// them, so nothing reads or writes them.
var buckets = []string{
	"stats",
	"stats_by_chat",
	"members",
	"members_by_chat",
	"chats",
	"dice_leaderboard",
	"quiz_leaderboard",
	"stats_month",
	"stats_month_idx",
	"stats_month_state",
	"stats_month_summary",
	"stats_daily",
	"stats_month_imported_ids",
	"reputation",
	"captcha",
	"captcha_user_idx",
	"gracekick",
	"referral_services",
	"referral_services_name_idx",
	"referrals",
	"admission_attempts",
	"deferred_jobs",
	"tiktok_videos",
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

// NewID returns a 16-char hex string (8 random bytes) suitable for
// embedding into short-lived identifiers (battle ids, etc.). Callable
// without holding the DB so callers can prepare a record then write once.
func NewID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("random id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
