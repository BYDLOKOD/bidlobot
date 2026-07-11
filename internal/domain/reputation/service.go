package reputation

import (
	"context"
	"fmt"
)

var defaultStore Store

// SetStore configures the package-level store used by Apply, Balance,
// and Leaderboard. Must be called before any domain function, typically
// during wiring in cmd/bidlobot/main.go.
func SetStore(s Store) {
	if s == nil {
		panic("reputation: nil store")
	}
	defaultStore = s
}

// Apply delegates to the package-level Store. See Store.Apply.
func Apply(ctx context.Context, absChatID, actorID, targetID int64, kind Kind, actorIsAdmin, targetIsAdmin bool) (Result, error) {
	if defaultStore == nil {
		return Result{}, fmt.Errorf("reputation: store not set")
	}
	return defaultStore.Apply(ctx, absChatID, actorID, targetID, kind, actorIsAdmin, targetIsAdmin)
}

// Balance delegates to the package-level Store. See Store.Balance.
func Balance(ctx context.Context, absChatID, userID int64, isAdmin bool) (int, error) {
	if defaultStore == nil {
		return 0, fmt.Errorf("reputation: store not set")
	}
	return defaultStore.Balance(ctx, absChatID, userID, isAdmin)
}

// Leaderboard delegates to the package-level Store. See Store.Leaderboard.
func Leaderboard(ctx context.Context, absChatID int64, limit int) ([]Entry, error) {
	if defaultStore == nil {
		return nil, fmt.Errorf("reputation: store not set")
	}
	return defaultStore.Leaderboard(ctx, absChatID, limit)
}
