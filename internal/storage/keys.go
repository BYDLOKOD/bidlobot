package storage

import "fmt"

func StatsKey(userID, absChatID int64) []byte {
	return []byte(fmt.Sprintf("s:%020d:%020d", userID, absChatID))
}

func StatsChatIndex(absChatID, userID int64) []byte {
	return []byte(fmt.Sprintf("sc:%020d:%020d", absChatID, userID))
}

func StatsChatPrefix(absChatID int64) []byte {
	return []byte(fmt.Sprintf("sc:%020d:", absChatID))
}

func WarnKey(uuid string) []byte {
	return []byte(fmt.Sprintf("w:%s", uuid))
}

func WarnTargetIndex(absChatID, targetUserID int64, uuid string) []byte {
	return []byte(fmt.Sprintf("wt:%020d:%020d:%s", absChatID, targetUserID, uuid))
}

func WarnTargetPrefix(absChatID, targetUserID int64) []byte {
	return []byte(fmt.Sprintf("wt:%020d:%020d:", absChatID, targetUserID))
}

func MemberKey(userID, absChatID int64) []byte {
	return []byte(fmt.Sprintf("m:%020d:%020d", userID, absChatID))
}

func MemberChatIndex(absChatID, userID int64) []byte {
	return []byte(fmt.Sprintf("mc:%020d:%020d", absChatID, userID))
}

func MemberChatPrefix(absChatID int64) []byte {
	return []byte(fmt.Sprintf("mc:%020d:", absChatID))
}

func ChatKey(absChatID int64) []byte {
	return []byte(fmt.Sprintf("c:%020d", absChatID))
}

// DMSessionKey maps an admin's private-chat user id to their selected
// target chat. One session per admin: managing two chats means
// re-selecting, which keeps "which chat am I about to act in"
// unambiguous.
func DMSessionKey(adminUserID int64) []byte {
	return []byte(fmt.Sprintf("dm:%020d", adminUserID))
}

// --- Monthly statistics (Workstream A) ---
//
// Month is a fixed 7-char "YYYY-MM" which is already lexicographically
// sortable, so it sits between the chat id and the user id. A month
// prefix scan therefore returns one calendar month's rows in user order.
// The per-(chat,month) MonthMeta singleton is stored under the userID 0
// sentinel: 0 zero-pads to all-zeros and so sorts first within the month
// prefix, ahead of every real user id.

// MonthStatsKey is the per-(chat, month, user) aggregate row. userID 0 is
// the MonthMeta sentinel for that (chat, month).
func MonthStatsKey(absChatID int64, month string, userID int64) []byte {
	return []byte(fmt.Sprintf("ms:%020d:%s:%020d", absChatID, month, userID))
}

// MonthStatsMonthPrefix scans every row (MonthMeta + all users) of one
// (chat, month).
func MonthStatsMonthPrefix(absChatID int64, month string) []byte {
	return []byte(fmt.Sprintf("ms:%020d:%s:", absChatID, month))
}

// MonthStatsChatIndex is a value-less key recording that a chat has data
// for a month (drives /stats months and the months menu cheaply).
func MonthStatsChatIndex(absChatID int64, month string) []byte {
	return []byte(fmt.Sprintf("msi:%020d:%s", absChatID, month))
}

// MonthStatsChatIndexPrefix scans every month a chat has any data for.
func MonthStatsChatIndexPrefix(absChatID int64) []byte {
	return []byte(fmt.Sprintf("msi:%020d:", absChatID))
}

// MonthStatsStateKey is the per-chat import/seal ledger singleton.
func MonthStatsStateKey(absChatID int64) []byte {
	return []byte(fmt.Sprintf("mss:%020d", absChatID))
}

// MonthStatsSummaryKey is the memoized rendered leaderboard for a sealed
// (chat, month).
func MonthStatsSummaryKey(absChatID int64, month string) []byte {
	return []byte(fmt.Sprintf("msum:%020d:%s", absChatID, month))
}

// ImportStateKey maps an admin's private-chat user id to a short-lived
// "awaiting an export file" state (Workstream B). Separate from
// DMSessionKey: the chat-selection session and the import-awaiting state
// have unrelated lifecycles and TTLs.
func ImportStateKey(adminUserID int64) []byte {
	return []byte(fmt.Sprintf("imp:%020d", adminUserID))
}

// GraceKickKey is one open grace ticket for (chat, user). The chat id
// leads so GraceKickChatPrefix scans a whole chat's open tickets in one
// cursor pass during the daily sweep.
func GraceKickKey(absChatID, userID int64) []byte {
	return []byte(fmt.Sprintf("gk:%020d:%020d", absChatID, userID))
}

func GraceKickChatPrefix(absChatID int64) []byte {
	return []byte(fmt.Sprintf("gk:%020d:", absChatID))
}

// CaptchaKey is one open captcha challenge, keyed by its 16-hex id.
func CaptchaKey(challengeID string) []byte {
	return []byte(fmt.Sprintf("cc:%s", challengeID))
}

// CaptchaUserIndex lets GetByUser find the open challenge for (chat, user)
// in one read - the value is the challenge id. Used by OnJoin to drop a
// stale challenge when a user rejoins before the timeout fires.
func CaptchaUserIndex(absChatID, userID int64) []byte {
	return []byte(fmt.Sprintf("ccu:%020d:%020d", absChatID, userID))
}

func AbsChatID(chatID int64) int64 {
	if chatID < 0 {
		return -chatID
	}
	return chatID
}

func parseID(b []byte) int64 {
	var n int64
	for _, c := range b {
		if c >= '0' && c <= '9' {
			n = n*10 + int64(c-'0')
		}
	}
	return n
}
