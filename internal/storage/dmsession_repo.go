package storage

import (
	"context"
	"encoding/json"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/domain/dmsession"
)

var bktDMSessions = []byte("dm_sessions")

type DMSessionRepo struct {
	db *bolt.DB
}

func NewDMSessionRepo(db *bolt.DB) *DMSessionRepo {
	return &DMSessionRepo{db: db}
}

func (r *DMSessionRepo) Set(_ context.Context, adminUserID, absChatID int64, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	s := dmsession.Session{
		AdminUserID: adminUserID,
		AbsChatID:   absChatID,
		SelectedAt:  now.UTC(),
	}
	data, err := json.Marshal(&s)
	if err != nil {
		return err
	}
	return r.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bktDMSessions).Put(DMSessionKey(adminUserID), data)
	})
}

func (r *DMSessionRepo) Get(_ context.Context, adminUserID int64) (*dmsession.Session, error) {
	var s dmsession.Session
	err := r.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(bktDMSessions).Get(DMSessionKey(adminUserID))
		if data == nil {
			return dmsession.ErrNoSession
		}
		return json.Unmarshal(data, &s)
	})
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *DMSessionRepo) Clear(_ context.Context, adminUserID int64) error {
	return r.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bktDMSessions).Delete(DMSessionKey(adminUserID))
	})
}
