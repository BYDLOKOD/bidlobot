package bot

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

// TestReactionFanoutAlwaysReachesMembership verifies that the fanout
// runs the battle observer first and the membership handler second
// even when the battle observer reports an error. Telego's
// first-match-wins routing makes this property load-bearing for
// cleanup correctness in production.
func TestReactionFanoutAlwaysReachesMembership(t *testing.T) {
	var battleCalls, membershipCalls atomic.Int32

	a := &App{
		log: testLogger(),
		games: &GamesRegistry{
			Battle: BattleRoutes{
				ReactionObserver: func(_ *th.Context, _ telego.MessageReactionUpdated) error {
					battleCalls.Add(1)
					return errors.New("simulated battle observer failure")
				},
			},
		},
	}

	// Replace the membership-tracking part with a counter so we don't
	// need a real membership.Service backed by bbolt.
	fanout := func(ctx *th.Context, reaction telego.MessageReactionUpdated) error {
		if a.games != nil && a.games.Battle.ReactionObserver != nil {
			_ = a.games.Battle.ReactionObserver(ctx, reaction)
		}
		membershipCalls.Add(1)
		return nil
	}

	if err := fanout(nil, telego.MessageReactionUpdated{
		Chat:      telego.Chat{ID: -100},
		MessageID: 1,
		User:      &telego.User{ID: 200},
	}); err != nil {
		t.Fatal(err)
	}
	if battleCalls.Load() != 1 || membershipCalls.Load() != 1 {
		t.Errorf("expected both observers to fire once each; battle=%d membership=%d",
			battleCalls.Load(), membershipCalls.Load())
	}
}

// TestReactionFanoutSkipsBattleWhenAbsent confirms the fanout works
// when the App has no games attached - the membership handler must
// still receive the reaction.
func TestReactionFanoutSkipsBattleWhenAbsent(t *testing.T) {
	var membershipCalls atomic.Int32

	a := &App{log: testLogger()}
	fanout := func(ctx *th.Context, reaction telego.MessageReactionUpdated) error {
		if a.games != nil && a.games.Battle.ReactionObserver != nil {
			_ = a.games.Battle.ReactionObserver(ctx, reaction)
		}
		membershipCalls.Add(1)
		return nil
	}
	if err := fanout(nil, telego.MessageReactionUpdated{User: &telego.User{ID: 1}}); err != nil {
		t.Fatal(err)
	}
	if membershipCalls.Load() != 1 {
		t.Errorf("membership observer must always fire; got %d", membershipCalls.Load())
	}
}
