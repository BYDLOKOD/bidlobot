package dice

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// RollOutcome is the result of attempting to record a roll. The handler
// uses it to decide whether to congratulate the user on a new chat
// record, mention they tied the existing one, or just announce the
// value without any record fanfare.
type RollOutcome struct {
	NewRecord bool    // Value > previous best (or no previous record existed)
	Tied      bool    // Value == previous best (and previous existed)
	Previous  *Record // nil when no record existed before this roll
	Recorded  Record  // the record state after Submit returns
}

// Service owns the leaderboard logic. It does not roll the dice itself;
// callers pass in the value Telegram returned in sendDice's Message.Dice.
type Service struct {
	store Store
	log   *slog.Logger
}

func NewService(store Store, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{store: store, log: log}
}

// SubmitRoll evaluates the new value against the current record and
// updates the leaderboard if appropriate. The returned RollOutcome
// describes how the value compared to the previous best so callers can
// render the right Russian text.
//
// Validation:
//   - emoji must be one of dice.AllowedEmojis
//   - value must be 1..MaxValue[emoji]
//   - userID must be non-zero
//   - absChatID must be non-zero
//
// The store is read in its own call and written only on a strict
// improvement. Two concurrent rolls of the same emoji in the same chat
// race; the later writer wins. That race is acceptable for a 200-member
// chat (collisions on the exact same tick are vanishingly rare and the
// worst outcome is a stale "new record" announcement).
func (s *Service) SubmitRoll(ctx context.Context, absChatID int64, emoji string, value int, userID int64, username, firstName string, ts time.Time) (*RollOutcome, error) {
	if absChatID == 0 {
		return nil, fmt.Errorf("dice: absChatID is zero")
	}
	if userID == 0 {
		return nil, fmt.Errorf("dice: userID is zero")
	}
	if !IsAllowedEmoji(emoji) {
		return nil, fmt.Errorf("dice: emoji %q is not allowed", emoji)
	}
	maxV := MaxValue[emoji]
	if value < 1 || value > maxV {
		return nil, fmt.Errorf("dice: value %d outside 1..%d for emoji %s", value, maxV, emoji)
	}
	if ts.IsZero() {
		ts = time.Now().UTC()
	} else {
		ts = ts.UTC()
	}

	prev, err := s.store.Get(ctx, absChatID, emoji)
	if err != nil && err != ErrNotFound {
		return nil, fmt.Errorf("dice: load record: %w", err)
	}

	candidate := Record{
		AbsChatID: absChatID,
		Emoji:     emoji,
		Value:     value,
		UserID:    userID,
		Username:  strings.ToLower(strings.TrimSpace(username)),
		FirstName: firstName,
		SetAt:     ts,
	}

	outcome := &RollOutcome{Previous: prev, Recorded: candidate}

	switch {
	case prev == nil:
		// First record on this emoji in this chat.
		if writeErr := s.store.Put(ctx, candidate); writeErr != nil {
			return nil, fmt.Errorf("dice: store record: %w", writeErr)
		}
		outcome.NewRecord = true
	case value > prev.Value:
		if writeErr := s.store.Put(ctx, candidate); writeErr != nil {
			return nil, fmt.Errorf("dice: store record: %w", writeErr)
		}
		outcome.NewRecord = true
	case value == prev.Value:
		// Tie - we deliberately keep the older record (fairness: first
		// to reach the top stays on top).
		outcome.Tied = true
		outcome.Recorded = *prev
	default:
		// Lower than current record - no write, just an outcome with
		// the existing top intact.
		outcome.Recorded = *prev
	}

	return outcome, nil
}

// Top returns the current record for (absChatID, emoji). nil + nil means
// "no record yet" (callers should render a friendly placeholder); a
// non-nil error means the storage call failed.
func (s *Service) Top(ctx context.Context, absChatID int64, emoji string) (*Record, error) {
	if absChatID == 0 {
		return nil, fmt.Errorf("dice: absChatID is zero")
	}
	if !IsAllowedEmoji(emoji) {
		return nil, fmt.Errorf("dice: emoji %q is not allowed", emoji)
	}
	r, err := s.store.Get(ctx, absChatID, emoji)
	if err == ErrNotFound {
		return nil, nil
	}
	return r, err
}
