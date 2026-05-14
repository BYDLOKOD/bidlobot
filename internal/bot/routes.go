package bot

import (
	"log/slog"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/moderation"
	"github.com/veschin/bidlobot/internal/domain/stats"
)

// reactionFanout returns the single message_reaction handler that runs
// every observer the bot needs: membership tracking (always), plus the
// battle observer (when games are wired). Telego routes a reaction to
// the first matching handler only, so we cannot register them as
// siblings - one would mask the other.
func reactionFanout(a *App, log *slog.Logger) th.MessageReactionHandler {
	membership := membershipReactionHandler(a.memberSvc, log)
	return func(ctx *th.Context, reaction telego.MessageReactionUpdated) error {
		// Battle observer first: short-circuits cheaply when the
		// reaction is not for an active battle. Errors are logged by
		// the observer; we never let them prevent the membership
		// observer from running.
		if a.games != nil && a.games.Battle.ReactionObserver != nil {
			if err := a.games.Battle.ReactionObserver(ctx, reaction); err != nil {
				log.Warn("battle reaction observer failed", "error", err)
			}
		}
		return membership(ctx, reaction)
	}
}

func registerRoutes(
	bh *th.BotHandler,
	a *App,
	statsH *stats.Handler,
	modH *moderation.Handler,
) {
	bh.Use(loggingHandler(a.log))

	dmGroup := bh.Group(privatePredicate())
	dmGroup.HandleMessage(a.handleHelpDM, th.CommandEqual("help"))
	dmGroup.HandleMessage(a.handleHelpDM, th.CommandEqual("start"))

	sgGroup := bh.Group(supergroupPredicate(), notLinkedChannelPredicate())
	sgGroup.Use(membershipMessageMiddleware(a.memberSvc, a.log))
	sgGroup.Use(statsCountHandler(a.statsBuffer))

	sgGroup.HandleMessage(statsH.HandleStats, th.CommandEqual("stats"))
	sgGroup.HandleMessage(modH.HandleWarns, th.CommandEqual("warns"))
	sgGroup.HandleMessage(a.handleHelpSupergroup, th.CommandEqual("help"))

	adminGroup := sgGroup.Group()
	adminGroup.Use(adminCheckHandler(a.adminCache, a.bot))
	adminGroup.HandleMessage(modH.HandleWarn, th.CommandEqual("warn"))
	adminGroup.HandleMessage(modH.HandleMute, th.CommandEqual("mute"))
	adminGroup.HandleMessage(modH.HandleUnmute, th.CommandEqual("unmute"))
	adminGroup.HandleMessage(modH.HandleBan, th.CommandEqual("ban"))
	adminGroup.HandleMessage(modH.HandleUnban, th.CommandEqual("unban"))

	registerGameRoutes(bh, sgGroup, a)

	bh.HandleMessageReaction(reactionFanout(a, a.log), th.AnyMessageReaction())
	bh.HandleMyChatMemberUpdated(membershipMyChatMemberHandler(a.memberSvc, a, a.log))
	bh.HandleChatMemberUpdated(membershipChatMemberHandler(a.memberSvc, a.adminCache, a.log))

	if a.inlineSvc != nil {
		bh.HandleInlineQuery(a.inlineSvc.Handler())
	}

	if a.dispatcher != nil {
		bh.HandleCallbackQuery(a.dispatcher.Handle, th.AnyCallbackQueryWithMessage())
	}
}
