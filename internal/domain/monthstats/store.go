// Package monthstats is the retroactive per-calendar-month statistics
// engine. It is a parallel sibling to package stats (which stays
// lifetime-only and unchanged): the legacy chat-export.org analysis needs
// a fundamentally different shape - keyed by (chat, "YYYY-MM", user) with
// per-user message/char/entity/keyword counters plus a per-month longest
// message and totals - so bolting it onto stats.Stats would force a
// migration on every existing record for zero gain. Both the live
// message handler and the history importer feed the same additive
// counters through the same counting rules (see sample.go), so a chat's
// monthly numbers converge regardless of how the data arrived.
package monthstats

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by Store.GetMonth / GetState / GetSummary when
// the requested record does not exist.
var ErrNotFound = errors.New("monthstats not found")

// MetaUserID is the sentinel user id under which the per-(chat, month)
// MonthMeta singleton is stored. 0 zero-pads to all-zeros and so sorts
// ahead of every real user id within a month prefix scan.
const MetaUserID int64 = 0

// SummarySchemaVer is bumped whenever the rendered leaderboard format
// changes; a stored MonthSummary with a lower SchemaVer is ignored and
// rebuilt, so a render change needs no data migration.
const SummarySchemaVer = 1

// MonthUserStat is the per-(chat, month, user) aggregate. Every counter
// is additive across both the live and the import paths. Char counts are
// rune (code-point) based, never bytes - the legacy Clojure `(count s)`
// counted characters.
type MonthUserStat struct {
	AbsChatID    int64     `json:"abs_chat_id"`
	Month        string    `json:"month"` // "2006-01"
	UserID       int64     `json:"user_id"`
	MsgCount     int64     `json:"msg_count"`
	RuneCount    int64     `json:"rune_count"`
	CustomEmoji  int64     `json:"custom_emoji"`
	Code         int64     `json:"code"`
	Mention      int64     `json:"mention"`
	BotCommand   int64     `json:"bot_command"`
	KeywordCount int64     `json:"keyword_count"`
	FirstSeen    time.Time `json:"first_seen"` // earliest msg ts this month (tie-break)
}

// MonthMeta is the per-(chat, month) singleton: month totals and the
// single longest message. Stored under MetaUserID in the same bucket so
// it shares the month prefix scan and one transaction with the user rows.
type MonthMeta struct {
	AbsChatID      int64  `json:"abs_chat_id"`
	Month          string `json:"month"`
	TotalMsgs      int64  `json:"total_msgs"`  // legacy `all*`
	TotalRunes     int64  `json:"total_runes"` // legacy `allt*`
	LongestUserID  int64  `json:"longest_user_id"`
	LongestRunes   int64  `json:"longest_runes"`
	LongestExcerpt string `json:"longest_excerpt"` // truncated to <= LongestExcerptRunes
	LongestFull    bool   `json:"longest_full"`    // false if the excerpt was cut
}

// LongestExcerptRunes bounds the persisted longest-message excerpt so the
// buffer and DB never hold a multi-thousand-rune message. The ranking
// uses the true rune length (LongestRunes); only the displayed body is
// capped.
const LongestExcerptRunes = 400

// MonthState is the per-chat idempotency + seal ledger (singleton). It is
// what makes additive monthly counters safe to re-import: the importer
// skips any export message id <= ImportHWM and any message at or after
// LiveTrackStart (already counted live), so every message is counted
// exactly once across both paths.
type MonthState struct {
	AbsChatID      int64           `json:"abs_chat_id"`
	ImportHWM      int64           `json:"import_hwm"`       // highest export message id ingested
	ImportMinID    int64           `json:"import_min_id"`    // lowest id ever ingested
	ImportMaxTS    time.Time       `json:"import_max_ts"`    // newest ts seen by import
	Sealed         map[string]bool `json:"sealed"`           // month -> immutable
	LiveTrackStart time.Time       `json:"live_track_start"` // first live Add ts for this chat
	UpdatedAt      time.Time       `json:"updated_at"`
}

// MonthSummary is the memoized rendered leaderboard of a sealed month.
type MonthSummary struct {
	AbsChatID int64     `json:"abs_chat_id"`
	Month     string    `json:"month"`
	HTML      string    `json:"html"`
	BuiltAt   time.Time `json:"built_at"`
	SchemaVer int       `json:"schema_ver"`
}

// FlushKey identifies one buffered row. UserID == MetaUserID addresses
// the month's MonthMeta; any other UserID addresses a MonthUserStat.
type FlushKey struct {
	AbsChatID int64
	Month     string
	UserID    int64
}

// FlushDelta is the accumulated change for one FlushKey since the last
// flush. The Longest* fields are carried only on the MetaUserID key.
type FlushDelta struct {
	MsgDelta     int64
	RuneDelta    int64
	CustomEmoji  int64
	Code         int64
	Mention      int64
	BotCommand   int64
	KeywordDelta int64
	FirstSeen    time.Time

	LongestUserID  int64
	LongestRunes   int64
	LongestExcerpt string
	LongestFull    bool
}

// Store is the persistence surface. Flush is purely additive (counters
// summed, longest max-reduced); idempotency is the importer's job via
// MonthState, never the store's.
type Store interface {
	// GetMonth returns the MonthMeta and every MonthUserStat for one
	// (chat, month). meta is nil and stats is empty when the month has no
	// data; that is not an error.
	GetMonth(ctx context.Context, absChatID int64, month string) (meta *MonthMeta, stats []MonthUserStat, err error)
	// ListMonths returns every month a chat has data for, ascending.
	ListMonths(ctx context.Context, absChatID int64) ([]string, error)

	GetState(ctx context.Context, absChatID int64) (*MonthState, error)
	PutState(ctx context.Context, st *MonthState) error

	GetSummary(ctx context.Context, absChatID int64, month string) (*MonthSummary, error)
	PutSummary(ctx context.Context, s *MonthSummary) error

	// Flush applies the batch additively in a single transaction.
	Flush(ctx context.Context, batch map[FlushKey]*FlushDelta) error
}
