package main

import (
	"log/slog"

	"github.com/mymmrac/telego"
	"go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/bot"
	"github.com/veschin/bidlobot/internal/games/dice"
	"github.com/veschin/bidlobot/internal/storage"
)

// buildGames constructs the GamesRegistry that backs Phase 4 mini-games
// (dice, battle, quiz). Returns a registry that may have only a subset
// of fields populated as additional games land. The function is
// idempotent and has no side effects beyond the constructor calls; safe
// to invoke multiple times in tests.
//
// HOW TO WIRE THIS INTO THE BOT
//
// At runtime the registry is attached to the App via AttachGames. The
// expected placement in cmd/bidlobot/main.go (currently at line 89,
// right after the App is constructed) is:
//
//	app := bot.NewApp(tgBot, log, adminCache, statsBuffer, memberSvc, dispatcher, pendingRepo, inlineSvc)
//	app.AttachGames(buildGames(db, tgBot, log))   // <-- add this line
//
// AttachGames also wires the inline router into inlineSvc, so call
// AttachGames after the inlineSvc is already constructed (which is the
// case at line 80+).
//
// The wiring is split out so adding new games does not require editing
// main.go beyond the single AttachGames invocation.
func buildGames(db *bbolt.DB, tgBot *telego.Bot, log *slog.Logger) *bot.GamesRegistry {
	diceRepo := storage.NewDiceRepo(db)
	diceSvc := dice.NewService(diceRepo, log)
	diceHandler := bot.NewDiceHandler(diceSvc, tgBot, log)

	return &bot.GamesRegistry{
		Dice:         diceHandler,
		InlineRouter: bot.NewGamesInlineRouter(),
		// Battle and Quiz are added by their respective commits in
		// Phase 4. The shape of GamesRegistry stays stable; populating
		// the fields here is enough to enable them once they ship.
	}
}
