package bot

import (
	"context"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/moderation"
	"github.com/veschin/bidlobot/internal/domain/profile"
	"github.com/veschin/bidlobot/internal/domain/stats"
	"github.com/veschin/bidlobot/internal/storage"
	"github.com/veschin/bidlobot/internal/text"
)

func registerRoutes(
	bh *th.BotHandler,
	a *App,
	profileH *profile.Handler,
	statsH *stats.Handler,
	modH *moderation.Handler,
) {
	bh.Use(loggingHandler(a.log))

	dmGroup := bh.Group(privatePredicate())
	dmGroup.HandleMessage(profileH.HandleStartDM, th.CommandEqual("start"))
	dmGroup.HandleMessage(profileH.HandleCancelDM, th.CommandEqual("cancel"))
	dmGroup.HandleMessage(a.handleHelpDM, th.CommandEqual("help"))
	dmGroup.HandleMessage(profileH.HandleFSMInput, th.AnyMessageWithText())
	dmGroup.HandleMessage(profileH.HandleFSMNonText, th.Not(th.AnyMessageWithText()))
	dmGroup.HandleCallbackQuery(profileH.HandleFSMCallback, th.AnyCallbackQueryWithMessage())

	sgGroup := bh.Group(supergroupPredicate(), notLinkedChannelPredicate())
	sgGroup.Use(statsCountHandler(a.statsBuffer))

	sgGroup.HandleMessage(profileH.HandleRegister, th.CommandEqual("register"))
	sgGroup.HandleMessage(profileH.HandleProfile, th.CommandEqual("profile"))
	sgGroup.HandleMessage(profileH.HandleUpdate, th.CommandEqual("update"))
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

	bh.HandleMyChatMemberUpdated(func(_ *th.Context, mcu telego.ChatMemberUpdated) error {
		ctx := context.Background()
		chatID := mcu.Chat.ID
		newStatus := mcu.NewChatMember.MemberStatus()

		if mcu.Chat.Type != telego.ChatTypeSupergroup {
			if mcu.Chat.Type == telego.ChatTypeGroup {
				a.bot.SendMessage(ctx, &telego.SendMessageParams{
					ChatID: telego.ChatID{ID: chatID},
					Text:   text.MsgNotSupergroup,
				})
			}
			return nil
		}

		if newStatus == "administrator" {
			a.adminCache.Invalidate(storage.AbsChatID(chatID))
		} else if newStatus == "member" {
			a.bot.SendMessage(ctx, &telego.SendMessageParams{
				ChatID: telego.ChatID{ID: chatID},
				Text:   text.MsgNeedAdmin,
			})
		}
		return nil
	})

	bh.HandleChatMemberUpdated(func(_ *th.Context, cmu telego.ChatMemberUpdated) error {
		a.adminCache.Invalidate(storage.AbsChatID(cmu.Chat.ID))
		return nil
	})
}
