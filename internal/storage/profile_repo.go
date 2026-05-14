package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/domain/profile"
)

var (
	bktProfiles      = []byte("profiles")
	bktProfilesByChat = []byte("profiles_by_chat")
)

type ProfileRepo struct {
	db *bolt.DB
}

func NewProfileRepo(db *bolt.DB) *ProfileRepo {
	return &ProfileRepo{db: db}
}

func (r *ProfileRepo) Create(_ context.Context, p *profile.Profile) error {
	return r.db.Update(func(tx *bolt.Tx) error {
		bkt := tx.Bucket(bktProfiles)
		key := ProfileKey(p.UserID, p.ChatID)

		if bkt.Get(key) != nil {
			return profile.ErrExists
		}

		data, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if err := bkt.Put(key, data); err != nil {
			return err
		}

		idx := tx.Bucket(bktProfilesByChat)
		return idx.Put(ProfileChatIndex(p.ChatID, p.UserID), nil)
	})
}

func (r *ProfileRepo) Update(_ context.Context, p *profile.Profile) error {
	return r.db.Update(func(tx *bolt.Tx) error {
		bkt := tx.Bucket(bktProfiles)
		key := ProfileKey(p.UserID, p.ChatID)

		data, err := json.Marshal(p)
		if err != nil {
			return err
		}
		return bkt.Put(key, data)
	})
}

func (r *ProfileRepo) Get(_ context.Context, userID, absChatID int64) (*profile.Profile, error) {
	var p profile.Profile
	err := r.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(bktProfiles).Get(ProfileKey(userID, absChatID))
		if data == nil {
			return profile.ErrNotFound
		}
		return json.Unmarshal(data, &p)
	})
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *ProfileRepo) GetByUsername(_ context.Context, absChatID int64, username string) (*profile.Profile, error) {
	lower := strings.ToLower(username)
	var result *profile.Profile

	err := r.db.View(func(tx *bolt.Tx) error {
		profiles := tx.Bucket(bktProfiles)
		idx := tx.Bucket(bktProfilesByChat)
		prefix := ProfileChatPrefix(absChatID)
		c := idx.Cursor()

		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			parts := bytes.SplitN(k, []byte(":"), 3)
			if len(parts) < 3 {
				continue
			}
			userKey := append([]byte("p:"), parts[2]...)
			userKey = append(userKey, ':')
			userKey = append(userKey, parts[1]...)

			data := profiles.Get(ProfileKey(parseID(parts[2]), parseID(parts[1])))
			if data == nil {
				continue
			}
			var p profile.Profile
			if err := json.Unmarshal(data, &p); err != nil {
				continue
			}
			if strings.EqualFold(p.Username, lower) {
				result = &p
				return nil
			}
		}
		return profile.ErrNotFound
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (r *ProfileRepo) ListByChat(_ context.Context, absChatID int64) ([]profile.Profile, error) {
	var results []profile.Profile
	err := r.db.View(func(tx *bolt.Tx) error {
		profiles := tx.Bucket(bktProfiles)
		idx := tx.Bucket(bktProfilesByChat)
		prefix := ProfileChatPrefix(absChatID)
		c := idx.Cursor()

		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			parts := bytes.SplitN(k, []byte(":"), 3)
			if len(parts) < 3 {
				continue
			}
			data := profiles.Get(ProfileKey(parseID(parts[2]), parseID(parts[1])))
			if data == nil {
				continue
			}
			var p profile.Profile
			if err := json.Unmarshal(data, &p); err != nil {
				continue
			}
			results = append(results, p)
		}
		return nil
	})
	return results, err
}

func (r *ProfileRepo) ListByUser(_ context.Context, userID int64) ([]profile.Profile, error) {
	var results []profile.Profile
	err := r.db.View(func(tx *bolt.Tx) error {
		bkt := tx.Bucket(bktProfiles)
		prefix := ProfileUserPrefix(userID)
		c := bkt.Cursor()

		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			var p profile.Profile
			if err := json.Unmarshal(v, &p); err != nil {
				continue
			}
			results = append(results, p)
		}
		return nil
	})
	return results, err
}

func (r *ProfileRepo) Exists(_ context.Context, userID, absChatID int64) (bool, error) {
	var exists bool
	err := r.db.View(func(tx *bolt.Tx) error {
		exists = tx.Bucket(bktProfiles).Get(ProfileKey(userID, absChatID)) != nil
		return nil
	})
	return exists, err
}

func (r *ProfileRepo) UpdateUsernameAll(_ context.Context, userID int64, newUsername string) error {
	return r.db.Update(func(tx *bolt.Tx) error {
		bkt := tx.Bucket(bktProfiles)
		prefix := ProfileUserPrefix(userID)
		c := bkt.Cursor()

		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			var p profile.Profile
			if err := json.Unmarshal(v, &p); err != nil {
				continue
			}
			if p.Username == newUsername {
				continue
			}
			p.Username = newUsername
			data, err := json.Marshal(&p)
			if err != nil {
				continue
			}
			if err := bkt.Put(k, data); err != nil {
				return err
			}
		}
		return nil
	})
}

func parseID(b []byte) int64 {
	var n int64
	for _, c := range b {
		if c >= '0' && c <= '9' {
			n = n*10 + int64(c-'0')
		}
	}
	return n
}
