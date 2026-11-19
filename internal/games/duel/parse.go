package duel

import (
	"errors"
	"strings"
)

// ErrNoTarget - the command had no @username (and no reply target).
var ErrNoTarget = errors.New("duel: no opponent specified")

// ErrSelfTarget - the caller mentioned themselves.
var ErrSelfTarget = errors.New("duel: cannot duel yourself")

// ErrBotTarget - the mentioned handle is the bot itself.
var ErrBotTarget = errors.New("duel: cannot duel the bot")

// Opponent is the parsed challenge target. Username is without the
// leading '@', lowercased for self-comparison stability. Display keeps
// the original "@name" form for the announcement.
type Opponent struct {
	Username string
	Display  string
}

// ParseOpponent extracts the opponent handle from "/duel @user" text.
// callerUsername and botUsername are compared case-insensitively to
// reject self-duels and bot-duels. A leading '@' is optional in the
// argument but the token must otherwise look like a username (letters,
// digits, underscore, 1..32 chars per Telegram's rule).
//
// botUsername may be empty (bot identity unknown); the bot check is then
// skipped rather than erroring.
func ParseOpponent(text, callerUsername, botUsername string) (*Opponent, error) {
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return nil, ErrNoTarget
	}
	raw := strings.TrimSpace(fields[1])
	raw = strings.TrimPrefix(raw, "@")
	if !validUsername(raw) {
		return nil, ErrNoTarget
	}
	lower := strings.ToLower(raw)
	if callerUsername != "" && lower == strings.ToLower(strings.TrimPrefix(callerUsername, "@")) {
		return nil, ErrSelfTarget
	}
	if botUsername != "" && lower == strings.ToLower(strings.TrimPrefix(botUsername, "@")) {
		return nil, ErrBotTarget
	}
	return &Opponent{Username: lower, Display: "@" + raw}, nil
}

// validUsername applies Telegram's username charset rule (a..z, A..Z,
// 0..9, underscore; 1..32 chars). We do not enforce the "must start
// with a letter" / "no double underscore" sub-rules - this is a fun
// command, and an invalid handle simply fails to be a real user later;
// the goal here is to reject obvious garbage like a bare reason word.
func validUsername(s string) bool {
	if len(s) < 1 || len(s) > 32 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_':
		default:
			return false
		}
	}
	return true
}
