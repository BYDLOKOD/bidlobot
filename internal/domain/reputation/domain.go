package reputation

import "errors"

// Kind is the type of social-rating operation.
type Kind int

const (
	KindPraise Kind = iota
	KindRoast
)

// Result carries the new balances after a successful Apply call.
type Result struct {
	ActorBalance  int
	TargetBalance int
}

// Entry is one user's balance for the leaderboard / /reptop response.
type Entry struct {
	UserID  int64
	Balance int
}

// Sentinel validation errors. Each must be detectable via errors.Is.
var (
	ErrSelfTarget               = errors.New("reputation: cannot target self")
	ErrInsufficientBalance      = errors.New("reputation: insufficient balance")
	ErrTargetInsufficientBalance = errors.New("reputation: target balance insufficient")
)
