package hangman

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound signals that no round exists for the chat.
var ErrNotFound = errors.New("hangman: no round for chat")

// StaleAfter bounds how long an idle round survives before the next
// /hangman (no argument) may abandon and replace it.
const StaleAfter = time.Hour

// Round is the per-chat game state. Used holds the set of letters
// already guessed (correct or wrong); WrongCount is the subset that did
// not appear in Word. The word is stored uppercased.
type Round struct {
	AbsChatID  int64           `json:"abs_chat_id"`
	Word       string          `json:"word"`
	Used       map[string]bool `json:"used"`
	WrongCount int             `json:"wrong_count"`
	Active     bool            `json:"active"`
	StartedAt  time.Time       `json:"started_at"`
}

// Store is the persistence contract. The bbolt implementation lives in
// internal/storage; tests use an in-memory map.
type Store interface {
	GetRound(ctx context.Context, absChatID int64) (*Round, error)
	PutRound(ctx context.Context, r Round) error
	DeleteRound(ctx context.Context, absChatID int64) error
}
