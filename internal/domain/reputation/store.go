package reputation

import "context"

// Store is the persistence contract for the reputation domain.
// Implementations must be safe for concurrent use and durable.
type Store interface {
	// Wallet returns the current wallet and title state for a user in a
	// chat. An absent record yields the new-member defaults: available
	// 10, reserved 0, score 0. It is a read and does not fabricate
	// activity history.
	Wallet(ctx context.Context, chatID, userID int64) (Account, error)

	// Settle applies one praise or roast atomically. It enforces the
	// per-pair and per-sender daily limits, deduplicates the source
	// event, debits the actor and applies the target's wallet and title
	// changes in one transaction. Validation errors are the sentinel
	// errors in this package.
	Settle(ctx context.Context, req SocialRequest) (Result, error)

	// Leaderboard returns scored members for a chat, sorted by title
	// score descending then user ID ascending. limit<=0 returns all.
	Leaderboard(ctx context.Context, chatID int64, limit int) ([]Entry, error)
}
