package hangman

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"sort"
	"strings"
	"time"
)

// StartOutcome is the result of attempting to start a round.
type StartOutcome struct {
	Started  bool
	Recycled bool   // a stale round was abandoned and replaced
	Existing *Round // non-nil when Started is false (round already live)
}

// GuessResult enumerates the outcome of a single letter guess.
type GuessResult int

const (
	// GuessHit - the letter is in the word and was not used before.
	GuessHit GuessResult = iota
	// GuessMiss - the letter is not in the word (wrong count bumped).
	GuessMiss
	// GuessWon - this guess revealed the last hidden letter.
	GuessWon
	// GuessLost - this miss reached MaxWrong.
	GuessLost
)

// GuessOutcome is the full picture after a guess: the result, the round
// state used to render the board, and the secret word (filled on a
// terminal result so the announcement can show it).
type GuessOutcome struct {
	Result      GuessResult
	Word        string   // revealed only on GuessWon / GuessLost
	Masked      string   // word with unguessed letters as "_"
	UsedLetters []string // sorted, for display
	WrongLeft   int      // remaining wrong-guess budget
}

// Service owns the round lifecycle. It does not parse Telegram messages;
// the handler extracts the single-letter argument.
type Service struct {
	store Store
	rnd   *rand.Rand
	log   *slog.Logger
}

// NewService wires the service. rnd may be nil; Start then uses a fresh
// time-seeded source per call (acceptable for the low call volume).
func NewService(store Store, rnd *rand.Rand, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{store: store, rnd: rnd, log: log}
}

// Status returns the active round or nil when none is active.
func (s *Service) Status(ctx context.Context, absChatID int64) (*Round, error) {
	if absChatID == 0 {
		return nil, fmt.Errorf("hangman: absChatID is zero")
	}
	r, err := s.store.GetRound(ctx, absChatID)
	if err == ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("hangman: load round: %w", err)
	}
	if !r.Active {
		return nil, nil
	}
	return r, nil
}

// Start creates a new round. An active non-stale round is left intact
// and returned via StartOutcome.Existing. A round older than StaleAfter
// is replaced (Recycled=true).
func (s *Service) Start(ctx context.Context, absChatID int64, now time.Time) (*StartOutcome, error) {
	if absChatID == 0 {
		return nil, fmt.Errorf("hangman: absChatID is zero")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	existing, err := s.store.GetRound(ctx, absChatID)
	if err != nil && err != ErrNotFound {
		return nil, fmt.Errorf("hangman: load round: %w", err)
	}

	recycled := false
	if err == nil && existing.Active {
		if now.Sub(existing.StartedAt) < StaleAfter {
			return &StartOutcome{Started: false, Existing: existing}, nil
		}
		recycled = true
	}

	round := Round{
		AbsChatID:  absChatID,
		Word:       PickWord(s.rnd),
		Used:       make(map[string]bool),
		WrongCount: 0,
		Active:     true,
		StartedAt:  now,
	}
	if err := s.store.PutRound(ctx, round); err != nil {
		return nil, fmt.Errorf("hangman: store round: %w", err)
	}
	return &StartOutcome{Started: true, Recycled: recycled}, nil
}

// ErrBadLetter is returned for multi-character or non-letter guesses so
// the handler can show a hint instead of consuming a wrong guess.
var ErrBadLetter = fmt.Errorf("hangman: guess must be a single letter")

// ErrAlreadyUsed signals the letter was already guessed; the handler
// nudges the user without consuming the wrong-guess budget.
var ErrAlreadyUsed = fmt.Errorf("hangman: letter already used")

// Guess applies a single-letter guess to the active round. raw is the
// user's untrimmed token; it is validated to a single Latin/Cyrillic
// letter. Returns ErrNotFound (no round), ErrBadLetter (not a letter),
// or ErrAlreadyUsed (repeat) without mutating the wrong-guess budget in
// the latter two cases.
func (s *Service) Guess(ctx context.Context, absChatID int64, raw string) (*GuessOutcome, error) {
	if absChatID == 0 {
		return nil, fmt.Errorf("hangman: absChatID is zero")
	}
	letter := strings.TrimSpace(raw)
	if !IsSingleLetter(letter) {
		return nil, ErrBadLetter
	}
	letter = NormalizeLetter(letter)

	round, err := s.store.GetRound(ctx, absChatID)
	if err == ErrNotFound {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("hangman: load round: %w", err)
	}
	if !round.Active {
		return nil, ErrNotFound
	}
	if round.Used == nil {
		round.Used = make(map[string]bool)
	}
	if round.Used[letter] {
		return nil, ErrAlreadyUsed
	}

	round.Used[letter] = true
	hit := strings.Contains(round.Word, letter)
	if !hit {
		round.WrongCount++
	}

	if !hit && round.WrongCount >= MaxWrong {
		round.Active = false
		if err := s.store.DeleteRound(ctx, absChatID); err != nil {
			return nil, fmt.Errorf("hangman: end round: %w", err)
		}
		return &GuessOutcome{
			Result:      GuessLost,
			Word:        round.Word,
			Masked:      maskWord(round.Word, round.Used),
			UsedLetters: sortedUsed(round.Used),
			WrongLeft:   0,
		}, nil
	}

	if hit && fullyRevealed(round.Word, round.Used) {
		round.Active = false
		if err := s.store.DeleteRound(ctx, absChatID); err != nil {
			return nil, fmt.Errorf("hangman: end round: %w", err)
		}
		return &GuessOutcome{
			Result:      GuessWon,
			Word:        round.Word,
			Masked:      round.Word,
			UsedLetters: sortedUsed(round.Used),
			WrongLeft:   MaxWrong - round.WrongCount,
		}, nil
	}

	if err := s.store.PutRound(ctx, *round); err != nil {
		return nil, fmt.Errorf("hangman: store round: %w", err)
	}
	res := GuessHit
	if !hit {
		res = GuessMiss
	}
	return &GuessOutcome{
		Result:      res,
		Masked:      maskWord(round.Word, round.Used),
		UsedLetters: sortedUsed(round.Used),
		WrongLeft:   MaxWrong - round.WrongCount,
	}, nil
}

// MaskFor renders the current masked word for an active round (used by
// /hangman when a round is already running to show status).
func MaskFor(r *Round) string {
	if r == nil {
		return ""
	}
	return maskWord(r.Word, r.Used)
}

// SortedUsed exposes the sorted used-letter slice for status rendering.
func SortedUsed(r *Round) []string {
	if r == nil {
		return nil
	}
	return sortedUsed(r.Used)
}

// maskWord replaces every not-yet-guessed letter with "_", keeping any
// non-letter characters (defensive: the curated list is letters-only).
func maskWord(word string, used map[string]bool) string {
	var b strings.Builder
	for _, r := range word {
		ch := string(r)
		if used[strings.ToUpper(ch)] {
			b.WriteString(ch)
		} else {
			b.WriteString("_")
		}
	}
	return b.String()
}

// fullyRevealed reports whether every letter of word has been guessed.
func fullyRevealed(word string, used map[string]bool) bool {
	for _, r := range word {
		if !used[strings.ToUpper(string(r))] {
			return false
		}
	}
	return true
}

func sortedUsed(used map[string]bool) []string {
	out := make([]string, 0, len(used))
	for k := range used {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
