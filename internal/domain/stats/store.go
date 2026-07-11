package stats

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("stats not found")

type Stats struct {
	UserID       int64     `json:"user_id"`
	ChatID       int64     `json:"chat_id"`
	MessageCount int64     `json:"message_count"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
}

type FlushKey struct {
	UserID    int64
	AbsChatID int64
}

type FlushDelta struct {
	CountDelta int64
	FirstSeen  time.Time
	LastSeen   time.Time
}

type Store interface {
	Get(ctx context.Context, userID, absChatID int64) (*Stats, error)
	ListByChat(ctx context.Context, absChatID int64) ([]Stats, error)
	Flush(ctx context.Context, batch map[FlushKey]*FlushDelta) error
	FlushAtomic(ctx context.Context, lifetime map[FlushKey]*FlushDelta, daily map[string]map[FlushKey]*FlushDelta) error
	GetDaily(ctx context.Context, absChatID int64, day string) (map[int64]*Stats, error)
	FlushDaily(ctx context.Context, batch map[FlushKey]*FlushDelta, day string) error
}
