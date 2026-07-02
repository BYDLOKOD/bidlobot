package captcha

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by Store.Get / GetByUser when no open challenge
// matches the lookup key (solved, swept, or never existed).
var ErrNotFound = errors.New("captcha: challenge not found")

// Store persists open captcha challenges. A bbolt repo and an in-memory
// fake both satisfy it; the domain layer never imports a storage backend.
type Store interface {
	// Create stores a new challenge. A challenge with the same ID must not
	// already exist (IDs are crypto-random 16-hex; collisions do not happen).
	Create(ctx context.Context, c Challenge) error

	// Get returns the open challenge by its ID, or ErrNotFound.
	Get(ctx context.Context, id string) (*Challenge, error)

	// Delete removes a challenge by ID. Missing IDs are a silent no-op.
	Delete(ctx context.Context, id string) error

	// GetByUser returns the open challenge for (absChatID, userID), or
	// ErrNotFound. Used by OnJoin to drop a stale challenge on rejoin
	// before the timeout fires (a user can be kicked, rejoin, and get a
	// second captcha while the first is still in the store).
	GetByUser(ctx context.Context, absChatID, userID int64) (*Challenge, error)

	// ListExpired returns every open challenge whose ExpiresAt is before
	// now. The sweeper runs this each tick to kick the users who never
	// answered. A full bucket scan is fine: per-chat captcha counts are
	// tiny (a few concurrent joins at most).
	ListExpired(ctx context.Context, now time.Time) ([]Challenge, error)
}
