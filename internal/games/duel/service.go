// Package duel implements the /duel @user dice duel: the caller
// challenges a mentioned member, the bot rolls one die for each, and the
// higher roll wins. Resolution is immediate (no accept step, hence no
// persisted state and no callback) - the handler rolls both dice in
// sequence via the shared rate-limited sender and then asks this service
// to decide the winner.
//
// Keeping the win decision in a domain service (rather than inline in
// the handler) makes the tie/higher logic unit-testable without a live
// Telegram bot.
package duel

import "fmt"

// Side identifies which duelist a result refers to.
type Side int

const (
	// SideTie - equal rolls, replay suggested.
	SideTie Side = iota
	// SideChallenger - the caller won.
	SideChallenger
	// SideOpponent - the mentioned user won.
	SideOpponent
)

// Result is the decided outcome of a duel given both dice values.
type Result struct {
	Winner        Side
	ChallengerVal int
	OpponentVal   int
}

// Decide compares two dice values (each the authoritative value Telegram
// returned from sendDice). Values are validated to the standard 1..6 die
// range; anything else is a programming error (the handler only ever
// passes a 🎲 roll) and returns an error rather than a silent default.
func Decide(challengerVal, opponentVal int) (*Result, error) {
	if challengerVal < 1 || challengerVal > 6 {
		return nil, fmt.Errorf("duel: challenger value %d outside 1..6", challengerVal)
	}
	if opponentVal < 1 || opponentVal > 6 {
		return nil, fmt.Errorf("duel: opponent value %d outside 1..6", opponentVal)
	}
	r := &Result{ChallengerVal: challengerVal, OpponentVal: opponentVal}
	switch {
	case challengerVal > opponentVal:
		r.Winner = SideChallenger
	case opponentVal > challengerVal:
		r.Winner = SideOpponent
	default:
		r.Winner = SideTie
	}
	return r, nil
}
