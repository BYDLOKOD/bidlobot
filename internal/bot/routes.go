package bot

import (
	"log/slog"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/stats"
)

// publicModerationCommands are the verbs that used to run as visible
// slash commands in the group. They are now DM-only: issuing one in the
// public chat deletes the command (best effort) and points the admin to
// the private console, so the timeline never carries moderation spam.
var publicModerationCommands = []string{"warn", "warns", "mute", "unmute", "ban", "unban", "cleanup"}

// reactionFanout returns the single message_reaction handler that runs
// every observer the bot needs: membership tracking (always), plus the
// battle observer (when games are wired). Telego routes a reaction to
// the first matching handler only, so we cannot register them as
// siblings - one would mask the other.
func reactionFanout(a *App, log *slog.Logger) th.MessageReactionHandler {
	membership := membershipReactionHandler(a.memberSvc, log)
	return func(ctx *th.Context, reaction telego.MessageReactionUpdated) error {
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
) {
	bh.Use(loggingHandler(a.log))

	// Private chat is the ONLY moderation control surface. The console
	// routes by command itself, so one message handler covers it.
	dmGroup := bh.Group(privatePredicate())
	if a.dmConsole != nil {
		dmGroup.HandleMessage(a.dmConsole.HandleMessage)
	} else {
		dmGroup.HandleMessage(a.handleHelpDM, th.CommandEqual("help"))
		dmGroup.HandleMessage(a.handleHelpDM, th.CommandEqual("start"))
	}

	sgGroup := bh.Group(supergroupPredicate(), notLinkedChannelPredicate())
	sgGroup.Use(membershipMessageMiddleware(a.memberSvc, a.log))
	sgGroup.Use(statsCountHandler(a.statsBuffer))

	// Stats stays public: it is read-only, not chat management. Help
	// stays public so members can discover the bot.
	sgGroup.HandleMessage(a.gateMsg("stats", 5*time.Second, statsH.HandleStats), th.CommandEqual("stats"))
	sgGroup.HandleMessage(a.handleHelpSupergroup, th.CommandEqual("help"))

	// Any moderation verb typed in the group is intercepted: the public
	// command is deleted (if the bot can) and the admin is redirected to
	// the private console. Moderation never executes from the public
	// surface anymore - this is the core of the privacy principle.
	for _, cmd := range publicModerationCommands {
		sgGroup.HandleMessage(a.redirectModerationToDM, th.CommandEqual(cmd))
	}

	registerGameRoutes(bh, sgGroup, a)

	bh.HandleMessageReaction(reactionFanout(a, a.log), th.AnyMessageReaction())
	bh.HandleMyChatMemberUpdated(membershipMyChatMemberHandler(a.memberSvc, a, a.log))
	bh.HandleChatMemberUpdated(membershipChatMemberHandler(a.memberSvc, a.adminCache, a.log))

	if a.inlineSvc != nil {
		bh.HandleInlineQuery(a.inlineSvc.Handler())
	}

	// DM-console callbacks (namespace "dm:"). The public dispatcher
	// ("v1:") is still wired for any non-moderation callback infra, but
	// no surface now feeds it destructive pendings.
	if a.dmConsole != nil {
		bh.HandleCallbackQuery(a.dmConsole.HandleCallback, dmCallbackPredicate())
	}
	if a.dispatcher != nil {
		bh.HandleCallbackQuery(a.dispatcher.Handle, th.AnyCallbackQueryWithMessage())
	}
}

// redirectModerationToDM keeps the public timeline clean: it deletes the
// admin's moderation command (best effort - needs Delete Messages) and
// DMs them a pointer to the private console. Moderation is never
// executed from here.
func (a *App) redirectModerationToDM(thctx *th.Context, msg telego.Message) error {
	ctx := thctx.Context()

	// Best-effort delete of the public command so it does not linger.
	_ = a.sender.DeleteMessage(ctx, &telego.DeleteMessageParams{
		ChatID:    telego.ChatID{ID: msg.Chat.ID},
		MessageID: msg.GetMessageID(),
	})

	if msg.From == nil {
		return nil
	}

	// DM the issuer. This only succeeds if they have started the bot;
	// if not, the deletion alone still protects the timeline and the
	// onboarding message (sent when the bot is promoted) tells admins
	// to open the DM. We do not fall back to a public message - that
	// would reintroduce exactly the spam we are removing.
	_, err := a.sender.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: msg.From.ID},
		Text:      msgPublicModerationRedirect,
		ParseMode: telego.ModeHTML,
	})
	if err != nil {
		a.log.Info("moderation redirect DM not delivered (admin has not started the bot)",
			"user_id", msg.From.ID, "error", err)
	}
	return nil
}
