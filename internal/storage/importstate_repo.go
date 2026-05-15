package storage

import (
	"context"
	"encoding/json"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/domain/dmsession"
)

var bktImportStates = []byte("import_states")

// ImportStateRepo persists the per-admin "awaiting an export upload"
// state under bucket "import_states", keyed by ImportStateKey. It mirrors
// DMSessionRepo's shape; the only addition is lazy expiry in Get (a TTL
// state is meaningless once stale, and bbolt has no native expiry).
type ImportStateRepo struct {
	db *bolt.DB
}

func NewImportStateRepo(db *bolt.DB) *ImportStateRepo {
	return &ImportStateRepo{db: db}
}

func (r *ImportStateRepo) Set(_ context.Context, s dmsession.ImportState) error {
	now := time.Now().UTC()
	if s.StartedAt.IsZero() {
		s.StartedAt = now
	}
	if s.ExpiresAt.IsZero() {
		s.ExpiresAt = now.Add(dmsession.ImportAwaitTTL)
	}
	s.StartedAt = s.StartedAt.UTC()
	s.ExpiresAt = s.ExpiresAt.UTC()
	data, err := json.Marshal(&s)
	if err != nil {
		return err
	}
	return r.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bktImportStates).Put(ImportStateKey(s.AdminUserID), data)
	})
}

// Get returns ErrNoImportAwait when the state is absent OR expired. An
// expired row is also deleted opportunistically so a forgotten /import
// does not leave a permanent key behind.
func (r *ImportStateRepo) Get(_ context.Context, adminUserID int64) (*dmsession.ImportState, error) {
	var s dmsession.ImportState
	expired := false
	err := r.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bktImportStates)
		data := b.Get(ImportStateKey(adminUserID))
		if data == nil {
			return dmsession.ErrNoImportAwait
		}
		if uerr := json.Unmarshal(data, &s); uerr != nil {
			return uerr
		}
		if time.Now().UTC().After(s.ExpiresAt) {
			expired = true
			return b.Delete(ImportStateKey(adminUserID))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if expired {
		return nil, dmsession.ErrNoImportAwait
	}
	return &s, nil
}

func (r *ImportStateRepo) Clear(_ context.Context, adminUserID int64) error {
	return r.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bktImportStates).Delete(ImportStateKey(adminUserID))
	})
}
