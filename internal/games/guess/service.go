package guess

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// StartOutcome is the result of attempting to start a round.
type StartOutcome struct {
	// Started is true when a fresh round was created. When false a round
	// was already active (Existing is non-nil) or a stale round was
	// recycled (Started true, Recycled true).
	Started  bool
	Recycled bool   // a stale round was abandoned and replaced
	Existing *Round // non-nil when Started is false (round already live)
}

// GuessOutcome describes the result of a single numeric guess.
type GuessOutcome struct {
	Correct  bool // the guess equals the secret; round is now ended
	TooLow   bool // guess < secret
	TooHigh  bool // guess > secret
	Secret   int  // filled only when Correct (for the announcement)
	Attempts int  // attempt count after this guess
}

// Service owns the round lifecycle and the optional win leaderboard. It
// does not parse Telegram messages; the handler extracts the numeric
// argument and the user identity.
type Service struct {
	store Store
	rnd   Rand
	log   *slog.Logger
}

// NewService wires the service. rnd may be nil only if the caller never
// calls Start (the handler always provides one); a nil rnd makes Start
// return an error rather than panic.
func NewService(store Store, rnd Rand, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{store: store, rnd: rnd, log: log}
}

// Status returns the active round for a chat, or nil when none is active.
// A finished (Active=false) round is reported as nil so callers treat it
// as "no round".
func (s *Service) Status(ctx context.Context, absChatID int64) (*Round, error) {
	if absChatID == 0 {
		return nil, fmt.Errorf("guess: absChatID is zero")
	}
	r, err := s.store.GetRound(ctx, absChatID)
	if err == ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("guess: load round: %w", err)
	}
	if !r.Active {
		return nil, nil
	}
	return r, nil
}

// Start creates a new round for the chat. If a round is already active
// and not stale, it is left intact and returned via StartOutcome.Existing
// (Started=false). A round older than StaleAfter is silently abandoned
// and replaced (Started=true, Recycled=true).
func (s *Service) Start(ctx context.Context, absChatID int64, now time.Time) (*StartOutcome, error) {
	if absChatID == 0 {
		return nil, fmt.Errorf("guess: absChatID is zero")
	}
	if s.rnd == nil {
		return nil, fmt.Errorf("guess: nil rand source")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	existing, err := s.store.GetRound(ctx, absChatID)
	if err != nil && err != ErrNotFound {
		return nil, fmt.Errorf("guess: load round: %w", err)
	}

	recycled := false
	if err == nil && existing.Active {
		if now.Sub(existing.StartedAt) < StaleAfter {
			return &StartOutcome{Started: false, Existing: existing}, nil
		}
		// Stale: fall through and overwrite, flagging the recycle so the
		// handler can tell the chat the old round was dropped.
		recycled = true
	}

	// rnd.Intn(n) yields [0, n); shift into [Min, Max].
	secret := Min + s.rnd.Intn(Max-Min+1)
	round := Round{
		AbsChatID: absChatID,
		Secret:    secret,
		Active:    true,
		StartedAt: now,
		Attempts:  0,
	}
	if err := s.store.PutRound(ctx, round); err != nil {
		return nil, fmt.Errorf("guess: store round: %w", err)
	}
	return &StartOutcome{Started: true, Recycled: recycled}, nil
}

// Guess evaluates a numeric guess against the active round. value must
// already be range-checked by the caller for the friendly out-of-range
// message; Guess re-validates defensively and rejects out-of-range with
// an error so a bypassed caller cannot corrupt the attempt count.
//
// On a correct guess the round is ended (deleted) and, when userID is
// non-zero, the win leaderboard is incremented. A leaderboard write
// failure is logged but does not fail the call - the win itself still
// stands.
func (s *Service) Guess(ctx context.Context, absChatID int64, value int, userID int64, username, firstName string, now time.Time) (*GuessOutcome, error) {
	if absChatID == 0 {
		return nil, fmt.Errorf("guess: absChatID is zero")
	}
	if value < Min || value > Max {
		return nil, fmt.Errorf("guess: value %d outside %d..%d", value, Min, Max)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	round, err := s.store.GetRound(ctx, absChatID)
	if err == ErrNotFound {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("guess: load round: %w", err)
	}
	if !round.Active {
		return nil, ErrNotFound
	}

	round.Attempts++

	switch {
	case value < round.Secret:
		if err := s.store.PutRound(ctx, *round); err != nil {
			return nil, fmt.Errorf("guess: store round: %w", err)
		}
		return &GuessOutcome{TooLow: true, Attempts: round.Attempts}, nil
	case value > round.Secret:
		if err := s.store.PutRound(ctx, *round); err != nil {
			return nil, fmt.Errorf("guess: store round: %w", err)
		}
		return &GuessOutcome{TooHigh: true, Attempts: round.Attempts}, nil
	}

	// Correct: end the round, then bump the leaderboard (best effort).
	if err := s.store.DeleteRound(ctx, absChatID); err != nil {
		return nil, fmt.Errorf("guess: end round: %w", err)
	}
	if userID != 0 {
		if winErr := s.store.IncrementWin(ctx, WinEntry{
			AbsChatID: absChatID,
			UserID:    userID,
			Username:  strings.ToLower(strings.TrimSpace(username)),
			FirstName: firstName,
			LastWonAt: now,
		}); winErr != nil {
			s.log.Warn("guess: IncrementWin failed", "error", winErr, "chat_id", absChatID, "user_id", userID)
		}
	}
	return &GuessOutcome{
		Correct:  true,
		Secret:   round.Secret,
		Attempts: round.Attempts,
	}, nil
}

// Top returns the chat's win leaderboard, highest first, capped to
// limit. A nil slice with nil error means "no wins yet".
func (s *Service) Top(ctx context.Context, absChatID int64, limit int) ([]WinEntry, error) {
	if absChatID == 0 {
		return nil, fmt.Errorf("guess: absChatID is zero")
	}
	return s.store.TopWins(ctx, absChatID, limit)
}
