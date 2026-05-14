package moderation

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("warning not found")

type Warning struct {
	ID           string    `json:"id"`
	TargetUserID int64     `json:"target_user_id"`
	ChatID       int64     `json:"chat_id"`
	IssuerUserID int64     `json:"issuer_user_id"`
	Reason       string    `json:"reason,omitempty"`
	Timestamp    time.Time `json:"timestamp"`
	Active       bool      `json:"active"`
}

type Store interface {
	CreateWarning(ctx context.Context, w *Warning) (activeCount int, err error)
	ListActive(ctx context.Context, targetUserID, absChatID int64) ([]Warning, error)
	CountActive(ctx context.Context, targetUserID, absChatID int64) (int, error)
	ClearWarnings(ctx context.Context, targetUserID, absChatID int64) error
}
