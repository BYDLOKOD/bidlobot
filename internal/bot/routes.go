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

// chatMemberFanout returns the single chat_member handler that runs both
// membership tracking (always) and the new-member captcha (when wired).
// Telego routes chat_member to the first matching handler only, so - like
// reactionFanout - both observers must share one entry point. The captcha
// handler is nil-tolerant: when the feature is off it is a pure
// pass-through to membership.
func chatMemberFanout(a *App, log *slog.Logger) th.ChatMemberUpdatedHandler {
	membership := membershipChatMemberHandler(a.memberSvc, a.adminCache, log)
	captcha := captchaChatMemberHandler(a.captchaSvc, log)
	return func(ctx *th.Context, cmu telego.ChatMemberUpdated) error {
		if err := membership(ctx, cmu); err != nil {
			log.Warn("membership chat-member handler failed", "error", err)
		}
		return captcha(ctx, cmu)
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
	// Avoid the typed-nil-interface trap: a nil *monthstats.Buffer boxed
	// into MonthlyIncrementer is a non-nil interface, so pass an untyped
	// nil when the monthly engine is not wired (e.g. minimal test apps).
	var monthly MonthlyIncrementer
	if a.monthBuffer != nil {
		monthly = a.monthBuffer
	}
	sgGroup.Use(statsCountHandler(a.statsBuffer, monthly))
	// Passive RAM recorder for the optional summarization feature. Like
	// the stats observer it must see the original human message, so it
	// sits among the observers, before the YouTube sanitizer (which
	// deletes+reposts). Wired only when the GLM feature is configured.
	if a.summarize != nil {
		sgGroup.Use(summarizeRecorder(a.summarize))
	}
	// Runs after the passive observers (membership/stats see the original
	// human message); it deletes+reposts a YouTube link carrying the si=
	// share-tracking param. nil-tolerant against a minimal test App.
	sgGroup.Use(youtubeSanitizer(a))

	// Stats stays public: it is read-only, not chat management. Help
	// stays public so members can discover the bot.
	sgGroup.HandleMessage(a.gateMsg("stats", 5*time.Second, statsH.HandleStats), th.CommandEqual("stats"))
	sgGroup.HandleMessage(a.handleHelpSupergroup, th.CommandEqual("help"))

	// /summarize is admin-only (checked inside the handler) but read-only
	// and not chat-management, so it stays a public command like /stats.
	// Registered even when the feature is off so the slash menu stays
	// honest - the handler replies "not configured" to admins. The
	// Cyrillic /итог alias uses textCommandPredicate, NOT th.CommandEqual:
	// the latter compiles to an ASCII-only RE2 \w regex that never
	// matches Cyrillic. It is typed-only (setMyCommands also rejects
	// non-ASCII names so it stays out of the menu). gateMsg shares one
	// per-user cooldown key across both spellings.
	sgGroup.HandleMessage(a.gateMsg("summarize", summarizeCooldown, a.handleSummarize), th.CommandEqual("summarize"))
	sgGroup.HandleMessage(a.gateMsg("summarize", summarizeCooldown, a.handleSummarize), textCommandPredicate("/итог"))

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
	bh.HandleChatMemberUpdated(chatMemberFanout(a, a.log))

	if a.inlineSvc != nil {
		bh.HandleInlineQuery(a.inlineSvc.Handler())
	}

	// Callback ordering matters: first match wins. The captcha predicate
	// ("cap:") must precede the catch-all "v1:" dispatcher, or a new
	// member's answer button would be swallowed by the "Кнопка устарела"
	// fallback. DM-console callbacks ("dm:") are private-chat-scoped and
	// never collide with either public namespace.
	if a.captchaSvc != nil {
		bh.HandleCallbackQuery(captchaCallbackHandler(a.captchaSvc, a.log), captchaCallbackPredicate())
	}
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
