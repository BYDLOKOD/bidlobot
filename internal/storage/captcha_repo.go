package storage

import (
	"context"
	"encoding/json"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/domain/captcha"
)

var (
	bktCaptcha     = []byte("captcha")
	bktCaptchaUser = []byte("captcha_user_idx")
)

// CaptchaRepo persists open captcha challenges under bucket "captcha",
// keyed by CaptchaKey (challenge id), with a secondary index
// (CaptchaUserIndex) so GetByUser is a single read. Create writes both in
// one transaction; Delete removes both.
type CaptchaRepo struct {
	db *bolt.DB
}

func NewCaptchaRepo(db *bolt.DB) *CaptchaRepo { return &CaptchaRepo{db: db} }

func (r *CaptchaRepo) Create(_ context.Context, c captcha.Challenge) error {
	c.CreatedAt = c.CreatedAt.UTC()
	c.ExpiresAt = c.ExpiresAt.UTC()
	data, err := json.Marshal(&c)
	if err != nil {
		return err
	}
	return r.db.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket(bktCaptcha).Put(CaptchaKey(c.ID), data); err != nil {
			return err
		}
		return tx.Bucket(bktCaptchaUser).Put(CaptchaUserIndex(c.AbsChatID, c.UserID), []byte(c.ID))
	})
}

func (r *CaptchaRepo) Get(_ context.Context, id string) (*captcha.Challenge, error) {
	var c captcha.Challenge
	err := r.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bktCaptcha).Get(CaptchaKey(id))
		if v == nil {
			return captcha.ErrNotFound
		}
		return json.Unmarshal(v, &c)
	})
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *CaptchaRepo) Delete(_ context.Context, id string) error {
	// The user index is keyed by (chat, user), not id; to remove it we
	// read the challenge first. A missing challenge is a silent no-op.
	var idx []byte
	_ = r.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bktCaptcha).Get(CaptchaKey(id))
		if v != nil {
			var c captcha.Challenge
			if json.Unmarshal(v, &c) == nil {
				idx = CaptchaUserIndex(c.AbsChatID, c.UserID)
			}
		}
		return nil
	})
	return r.db.Update(func(tx *bolt.Tx) error {
		_ = tx.Bucket(bktCaptcha).Delete(CaptchaKey(id))
		if idx != nil {
			_ = tx.Bucket(bktCaptchaUser).Delete(idx)
		}
		return nil
	})
}

func (r *CaptchaRepo) GetByUser(_ context.Context, absChatID, userID int64) (*captcha.Challenge, error) {
	var c captcha.Challenge
	err := r.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bktCaptchaUser).Get(CaptchaUserIndex(absChatID, userID))
		if v == nil {
			return captcha.ErrNotFound
		}
		data := tx.Bucket(bktCaptcha).Get(CaptchaKey(string(v)))
		if data == nil {
			// Orphaned index pointing at a deleted challenge. Treat as
			// not found; a subsequent Create overwrites the index.
			return captcha.ErrNotFound
		}
		return json.Unmarshal(data, &c)
	})
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// ListExpired returns every open challenge whose ExpiresAt is before now.
// A full bucket scan is fine: per-chat captcha counts are tiny (a handful
// of concurrent joins at most), so no prefix optimization is warranted.
func (r *CaptchaRepo) ListExpired(_ context.Context, now time.Time) ([]captcha.Challenge, error) {
	now = now.UTC()
	var out []captcha.Challenge
	err := r.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bktCaptcha).ForEach(func(_ /*key*/, v []byte) error {
			var c captcha.Challenge
			if uerr := json.Unmarshal(v, &c); uerr != nil {
				return uerr
			}
			if c.ExpiresAt.Before(now) {
				out = append(out, c)
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Compile-time check: CaptchaRepo satisfies captcha.Store.
var _ captcha.Store = (*CaptchaRepo)(nil)
