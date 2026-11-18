package reputation

import (
	"errors"
	"time"
)

// Kind is the type of social-rating operation.
type Kind int

const (
	KindPraise Kind = iota
	KindRoast
)

// SourceKind identifies the input that produced a social settlement.
// Slash commands and reactions share the same daily limits, so the
// source kind is part of the deduplication key.
type SourceKind string

const (
	SourceSlash    SourceKind = "slash"
	SourceReaction SourceKind = "reaction"
)

// Account is one member's wallet and title state in a chat.
//
// Available is the spendable, unreserved balance. Reserved is held for
// an accepted commitment and is not spendable. Score is the title
// score; only social settlement may change it.
type Account struct {
	ChatID    int64 `json:"-"`
	UserID    int64 `json:"-"`
	Available int   `json:"available"`
	Reserved  int   `json:"reserved"`
	Score     int   `json:"score"`
}

// Result carries the post-settlement state and the signed changes.
type Result struct {
	Actor  Account
	Target Account

	ActorWalletDelta  int
	TargetWalletDelta int
	TargetScoreDelta  int
}

// Entry is one scored member row for /reptop. Score is the title score;
// the wallet never participates in ordering.
type Entry struct {
	UserID int64
	Score  int
}

// SocialRequest is one atomic praise or roast settlement.
//
// SourceMessageID identifies the command or reacted message. A non-zero
// id makes the settlement idempotent per actor and source event. Now is
// the settlement instant; the zero value means time.Now(). It exists so
// tests can move across the UTC day boundary deterministically.
type SocialRequest struct {
	ChatID          int64
	ActorID         int64
	TargetID        int64
	Kind            Kind
	Source          SourceKind
	SourceMessageID int
	Now             time.Time
}

// Title is one cumulative achievement threshold. Activity states what
// the title unlocks in the chat, which /reptop shows next to the score.
type Title struct {
	Name      string
	Threshold int
	Activity  string
}

const (
	DutyThreshold   = 12
	MasterThreshold = 36
	ProdThreshold   = 72
)

// Titles are the launch thresholds from the target specification.
// Thresholds are cumulative: the highest reached title applies.
var Titles = []Title{
	{Name: "Участник", Threshold: 0, Activity: "крутить автомат, вступать в инциденты, торговать, арендовать и выращивать"},
	{Name: "Дежурный", Threshold: DutyThreshold, Activity: "открывать инцидент"},
	{Name: "Мастер", Threshold: MasterThreshold, Activity: "сдавать свои слоты производства"},
	{Name: "Хранитель прода", Threshold: ProdThreshold, Activity: "открывать дополнительную подсказку в инциденте"},
}

// TitleForScore returns the highest title whose threshold the score has
// reached. A negative score is treated as zero.
func TitleForScore(score int) Title {
	title := Titles[0]
	for _, candidate := range Titles[1:] {
		if score >= candidate.Threshold {
			title = candidate
			continue
		}
		break
	}
	return title
}

// NextTitle returns the next title above score and whether one exists.
// At the highest title the boolean is false.
func NextTitle(score int) (Title, bool) {
	for _, candidate := range Titles {
		if score < candidate.Threshold {
			return candidate, true
		}
	}
	return Title{}, false
}

// Sentinel validation errors. Each must be detectable via errors.Is.
var (
	ErrSelfTarget          = errors.New("reputation: cannot target self")
	ErrInsufficientBalance = errors.New("reputation: insufficient balance")
	ErrInsufficientReserve = errors.New("reputation: reservation cannot go below zero")
	ErrPairLimit           = errors.New("reputation: pair already settled today")
	ErrSenderLimit         = errors.New("reputation: sender daily limit reached")
	ErrDuplicateSource     = errors.New("reputation: source event already settled")
	ErrInvalidAmount       = errors.New("reputation: amount out of range")
)
