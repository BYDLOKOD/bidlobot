package pending

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("pending action not found")
	ErrExpired  = errors.New("pending action expired")
)

// Kind enumerates the destructive operations that need a two-step
// preview-then-confirm path through inline mode + callback_query.
type Kind string

const (
	KindWarn    Kind = "warn"
	KindMute    Kind = "mute"
	KindUnmute  Kind = "unmute"
	KindBan     Kind = "ban"
	KindUnban   Kind = "unban"
	KindCleanup Kind = "cleanup"
)

// Action carries everything an executor needs to perform a moderation
// or cleanup operation after the issuer has tapped Confirm. The ID is
// embedded into callback_data, so it must stay short - 16 hex chars
// (8 random bytes) leaves headroom for the "apply:" prefix inside the
// 64-byte callback_data limit.
type Action struct {
	ID            string        `json:"id"`
	Kind          Kind          `json:"kind"`
	AbsChatID     int64         `json:"abs_chat_id"`
	ActorUserID   int64         `json:"actor_user_id"`
	TargetUserID  int64         `json:"target_user_id,omitempty"`
	TargetDisplay string        `json:"target_display,omitempty"`
	Reason        string        `json:"reason,omitempty"`
	Duration      time.Duration `json:"duration,omitempty"`
	Threshold     time.Duration `json:"threshold,omitempty"`
	CreatedAt     time.Time     `json:"created_at"`
	ExpiresAt     time.Time     `json:"expires_at"`
}

// Store persists pending actions with explicit TTL semantics. Get must
// return ErrExpired (and remove the record) for expired entries so the
// caller never acts on stale data.
type Store interface {
	Create(ctx context.Context, a Action) error
	Get(ctx context.Context, id string) (*Action, error)
	Delete(ctx context.Context, id string) error
	// PinChatID locks the action to a chat on first callback. Subsequent
	// callbacks observed in a different chat must be refused - see the
	// callback dispatcher for the rationale (forward-attack guard).
	PinChatID(ctx context.Context, id string, absChatID int64) error
	// GarbageCollect removes all expired entries and returns the count
	// removed, so the periodic sweeper can log progress.
	GarbageCollect(ctx context.Context, now time.Time) (int, error)
}
