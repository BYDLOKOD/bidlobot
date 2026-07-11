package reputation

import "context"

// Store is the persistence contract for the reputation domain.
// Implementations must be safe for concurrent use and durable.
type Store interface {
	// Apply performs one atomic reputation operation inside a single
	// bbolt transaction. It lazily initializes actor and target with
	// the default balance (10 for regular, 20 for admin) on first
	// access. Returns a typed validation error (ErrSelfTarget,
	// ErrInsufficientBalance, ErrTargetInsufficientBalance) or a
	// Result on success.
	Apply(ctx context.Context, absChatID, actorID, targetID int64, kind Kind, actorIsAdmin, targetIsAdmin bool) (Result, error)

	// Balance returns the current balance for a user in a chat.
	// On first access, lazily initializes with the default balance
	// (10 for regular, 20 for admin). This modifies the store so
	// callers should use it intentionally.
	Balance(ctx context.Context, absChatID, userID int64, isAdmin bool) (int, error)

	// Leaderboard returns the top N entries for a chat, sorted by
	// balance descending then user ID ascending. limit<=0 returns all.
	Leaderboard(ctx context.Context, absChatID int64, limit int) ([]Entry, error)
}
