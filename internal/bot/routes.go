package bot

import (
	"log/slog"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

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

	// Private chat has no management console. Only help/start are
	// meaningful here: they show a brief intro and point to group use.
	privateGroup := bh.Group(privatePredicate())
	privateGroup.HandleMessage(a.handleHelpDM, th.CommandEqual("help"))
	privateGroup.HandleMessage(a.handleHelpDM, th.CommandEqual("start"))

	sgGroup := bh.Group(supergroupPredicate())

	// Game routes (dice, battle, quiz, 8ball, roast/praise, etc.)
	// share the supergroup tree so their slash commands and
	// callbacks coexist with stats/summarize/youtube.
	registerGameRoutes(bh, sgGroup, a)
	// Avoid the typed-nil-interface trap: a nil *monthstats.Buffer boxed
	// into MonthlyIncrementer is a non-nil interface, so pass an untyped
	// nil when the monthly engine is not wired (e.g. minimal test apps).
	var monthly MonthlyIncrementer
	if a.monthBuffer != nil {
		monthly = a.monthBuffer
	}
	sgGroup.Use(membershipMessageMiddleware(a.memberSvc, a.log))
	sgGroup.Use(statsCountHandler(a.statsBuffer, monthly))
	// Passive RAM recorder for the Pi/OMP summarization feature. Like
	// the stats observer it must see the original human message, so it
	// sits among the observers, before the YouTube sanitizer (which
	// deletes+reposts). Wired when a.summarize is set.
	if a.summarize != nil {
		sgGroup.Use(summarizeRecorder(a.summarize))
	}
	// Runs after the passive observers (membership/stats see the original
	// human message); it deletes+reposts a YouTube link carrying the si=
	// share-tracking param. nil-tolerant against a minimal test App.
	sgGroup.Use(youtubeSanitizer(a))

	// TikTok video repost: download, trim watermark end-screen, repost
	// attributed. Same privacy gate as the YouTube sanitizer.
	sgGroup.Use(tiktokReposter(a))

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
	bh.HandleMessageReaction(reactionFanout(a, a.log), th.AnyMessageReaction())
	bh.HandleMyChatMemberUpdated(membershipMyChatMemberHandler(a.memberSvc, a, a.log))
	bh.HandleChatMemberUpdated(chatMemberFanout(a, a.log))

	if a.inlineSvc != nil {
		bh.HandleInlineQuery(a.inlineSvc.Handler())
	}

	// Callback ordering matters: first match wins. The captcha predicate
	// ("cap:") must precede the catch-all "v1:" dispatcher, or a new
	// member's answer button would be swallowed by the "Кнопка устарела"
	// fallback.
	if a.captchaSvc != nil {
		bh.HandleCallbackQuery(captchaCallbackHandler(a.captchaSvc, a.log), captchaCallbackPredicate())
	}
	if a.dispatcher != nil {
		bh.HandleCallbackQuery(a.dispatcher.Handle, th.AnyCallbackQueryWithMessage())
	}
}
