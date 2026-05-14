package bot

import (
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/moderation"
	"github.com/veschin/bidlobot/internal/domain/stats"
)

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

	bh.HandleMessageReaction(membershipReactionHandler(a.memberSvc, a.log), th.AnyMessageReaction())
	bh.HandleMyChatMemberUpdated(membershipMyChatMemberHandler(a.memberSvc, a, a.log))
	bh.HandleChatMemberUpdated(membershipChatMemberHandler(a.memberSvc, a.adminCache, a.log))

	bh.HandleInlineQuery(inlineQueryHandler(a.log))
}
