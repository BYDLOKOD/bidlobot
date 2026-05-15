// Package dmsession persists which target chat an admin is currently
// managing from their private chat with the bot.
//
// The DM console is the only genuinely private control surface for a
// Telegram bot: inline results are posted publicly into the originating
// chat, so moderation cannot stay off the public timeline via inline.
// An admin DMs the bot, picks a chat once, then issues ban/warn/mute/
// cleanup against it without any of the 200 members seeing a thing.
package dmsession

import (
	"context"
	"errors"
	"time"
)

var ErrNoSession = errors.New("no active DM session - send /start to pick a chat")

// Session is the per-admin selection of a target chat. There is exactly
// one per admin user id; selecting another chat overwrites it.
type Session struct {
	AdminUserID int64     `json:"admin_user_id"`
	AbsChatID   int64     `json:"abs_chat_id"`
	SelectedAt  time.Time `json:"selected_at"`
}

// Store persists DM sessions. Implementations must treat a missing
// session as ErrNoSession (not a generic not-found) so the console can
// nudge the admin to /start.
type Store interface {
	Set(ctx context.Context, adminUserID, absChatID int64, now time.Time) error
	Get(ctx context.Context, adminUserID int64) (*Session, error)
	Clear(ctx context.Context, adminUserID int64) error
}
