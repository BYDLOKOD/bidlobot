package dmsession

import (
	"context"
	"errors"
	"time"
)

// ImportAwaitTTL is how long the bot waits for the export file after the
// admin runs /import. A chat-history export is large and the admin has to
// produce it in Telegram Desktop first, so the window is generous; it
// still expires so a forgotten /import does not silently swallow an
// unrelated document sent minutes later.
const ImportAwaitTTL = 10 * time.Minute

// ErrNoImportAwait is returned by ImportStateStore.Get when the admin has
// no live "awaiting export" state - either none was ever set, or it
// expired. The console maps this to "send /import first" rather than a
// generic error so a stray document does not look like a bug.
var ErrNoImportAwait = errors.New("no active import - send /import first")

// ImportState records that an admin ran /import and the bot is now
// waiting for them to upload the Telegram Desktop chat export. It binds
// the upload to a specific target chat (the admin's selected managed
// chat at /import time) so a document cannot be mis-attributed if the
// admin re-selects a different chat before uploading. There is exactly
// one per admin user id; a second /import overwrites it.
type ImportState struct {
	AdminUserID int64     `json:"admin_user_id"`
	AbsChatID   int64     `json:"abs_chat_id"`
	StartedAt   time.Time `json:"started_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// ImportStateStore persists the per-admin import-awaiting state.
// Implementations MUST treat a missing OR expired state as
// ErrNoImportAwait (lazy expiry: Get checks now > ExpiresAt and reports
// it as absent) so the console never acts on a stale window.
type ImportStateStore interface {
	Set(ctx context.Context, s ImportState) error
	Get(ctx context.Context, adminUserID int64) (*ImportState, error)
	Clear(ctx context.Context, adminUserID int64) error
}
