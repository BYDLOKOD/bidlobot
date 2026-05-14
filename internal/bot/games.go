// Package bot - mini-games wiring.
//
// GamesRegistry collects everything the mini-games subsystem needs to
// participate in the bot's existing handler set. It exists so cmd/bidlobot
// can construct the games once and hand a single struct to App, rather
// than threading three independent handlers through NewApp's signature.
package bot

import (
	th "github.com/mymmrac/telego/telegohandler"
)

// GamesRegistry bundles the per-game handlers and the inline router.
// Any field can be nil - registration code checks before wiring.
//
// The struct is shaped to accommodate the three games (dice, battle,
// quiz) defined in Phase 4. Battle and Quiz handlers + their reaction/
// callback observers are added by the respective game commits; the
// shape stays stable so cmd/bidlobot wiring evolves additively.
type GamesRegistry struct {
	// Dice handles "/dice" and "/dice <emoji>".
	Dice *DiceHandler

	// Battle handles "/battle X Y" and observes message_reaction
	// events to tally votes. nil until Game 2 lands.
	Battle BattleRoutes

	// Quiz handles "/quiz" / "/quiz top" and the callback when a user
	// taps a language-guess button. nil until Game 3 lands.
	Quiz QuizRoutes

	// InlineRouter is wired into InlineService.SetGameRouter so that
	// "@bidlobot dice/battle/quiz" inline queries surface as result
	// suggestions.
	InlineRouter InlineGameRouter
}

// BattleRoutes exposes only the surface registerGameRoutes needs from
// the battle wiring; the concrete BattleHandler ships with Game 2.
type BattleRoutes struct {
	Slash            th.MessageHandler
	ReactionObserver th.MessageReactionHandler
}

// QuizRoutes is the analogous shape for Game 3 (quiz). The callback
// handler is registered with a prefix predicate so quiz callbacks bypass
// the pending-action dispatcher.
type QuizRoutes struct {
	Slash             th.MessageHandler
	Callback          th.CallbackQueryHandler
	CallbackPredicate th.Predicate
}

// registerGameRoutes wires the per-game handlers into the existing
// supergroup handler tree. Called from registerRoutes so the routing
// table stays in one file.
func registerGameRoutes(bh *th.BotHandler, sgGroup *th.HandlerGroup, a *App) {
	g := a.games
	if g == nil {
		return
	}
	if g.Dice != nil {
		sgGroup.HandleMessage(g.Dice.HandleDice, th.CommandEqual("dice"))
	}
	if g.Battle.Slash != nil {
		sgGroup.HandleMessage(g.Battle.Slash, th.CommandEqual("battle"))
	}
	if g.Quiz.Slash != nil {
		sgGroup.HandleMessage(g.Quiz.Slash, th.CommandEqual("quiz"))
	}
	if g.Battle.ReactionObserver != nil {
		// Telego routes message_reaction to all matching handlers, so
		// the membership observer continues to fire alongside this one.
		bh.HandleMessageReaction(g.Battle.ReactionObserver, th.AnyMessageReaction())
	}
	if g.Quiz.Callback != nil && g.Quiz.CallbackPredicate != nil {
		// Quiz callbacks must be registered BEFORE the dispatcher so
		// telego's first-match-wins routing sends them here, not into
		// the pending-action lookup. routes.go calls this helper
		// before the dispatcher registration line.
		bh.HandleCallbackQuery(g.Quiz.Callback, g.Quiz.CallbackPredicate)
	}
}
