package main

import (
	"log/slog"

	"go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/bot"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/shared/tgclient"
	"github.com/veschin/bidlobot/internal/storage"
)

// buildGames constructs the public social-command registry. The generic
// mini-games were removed (TZ CUT-1); the only remaining surface is
// reputation: /praise, /roast, /rep, /reptop. The signature is kept as
// the single construction seam used by main, so startup wiring does not
// need to change. The former bot-username argument is unused now that
// /duel is gone.
func buildGames(db *bbolt.DB, sender *tgclient.Client, _ string, adminCache *shared.AdminCache, log *slog.Logger) *bot.GamesRegistry {
	return &bot.GamesRegistry{
		Reputation: bot.NewReputationHandler(sender, storage.NewReputationRepo(db), storage.NewMembershipRepo(db), log, adminCache),
	}
}
