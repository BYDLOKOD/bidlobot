// Package dice implements the chat dice game: roll Telegram's native
// animated dice and persist a per-chat per-emoji leaderboard so the chat
// can chase its own records.
//
// The bot does not roll its own random value - the dice value comes from
// Telegram's sendDice response, which is the authoritative source. The
// service only owns the "best score so far" record.
package dice

import (
	"context"
	"errors"
	"time"
)

// AllowedEmojis enumerates the dice variants Telegram's sendDice
// supports. The bot rejects anything outside this set so we cannot
// accidentally request a value for an unsupported emoji and surprise
// the user with an API error.
var AllowedEmojis = []string{
	"\U0001F3B2", // 🎲
	"\U0001F3AF", // 🎯
	"\U0001F3C0", // 🏀
	"⚽",     // ⚽
	"\U0001F3B3", // 🎳
	"\U0001F3B0", // 🎰
}

// MaxValue maps each allowed emoji to the maximum value sendDice can
// return for it. Used by tests and for sanity checks; we do not enforce
// it on the wire because Telegram is the source of truth.
var MaxValue = map[string]int{
	"\U0001F3B2": 6,  // 🎲
	"\U0001F3AF": 6,  // 🎯
	"\U0001F3C0": 5,  // 🏀
	"⚽":     5,  // ⚽
	"\U0001F3B3": 6,  // 🎳
	"\U0001F3B0": 64, // 🎰
}

// DefaultEmoji is what /dice with no argument rolls.
const DefaultEmoji = "\U0001F3B2" // 🎲

// ErrNotFound signals that the leaderboard has no record for the given
// (chat, emoji) pair. Treated as "no record yet" by callers.
var ErrNotFound = errors.New("dice: leaderboard record not found")

// Record is the per-(chat, emoji) leaderboard entry. The bot stores at
// most one Record per pair - the best score wins.
type Record struct {
	AbsChatID int64     `json:"abs_chat_id"`
	Emoji     string    `json:"emoji"`
	Value     int       `json:"value"`
	UserID    int64     `json:"user_id"`
	Username  string    `json:"username,omitempty"`
	FirstName string    `json:"first_name,omitempty"`
	SetAt     time.Time `json:"set_at"`
}

// Store is the persistence contract the dice service depends on. The
// bbolt-backed implementation lives in internal/storage; tests use a
// simple in-memory map.
type Store interface {
	// Get returns the current record for (absChatID, emoji) or
	// ErrNotFound when nothing has been stored yet.
	Get(ctx context.Context, absChatID int64, emoji string) (*Record, error)
	// Put unconditionally writes the record. Callers are expected to
	// have compared values first (the service does so).
	Put(ctx context.Context, r Record) error
}

// IsAllowedEmoji returns true when emoji is one of the six values
// Telegram's sendDice supports.
func IsAllowedEmoji(emoji string) bool {
	for _, e := range AllowedEmojis {
		if e == emoji {
			return true
		}
	}
	return false
}
