package bot

import (
	"context"
	"log/slog"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
	"github.com/veschin/bidlobot/internal/text"
)

// membershipMessageMiddleware records the sender of every supergroup
// message into the membership store. Runs alongside statsCountHandler so
// that the bot maintains a per-user activity record even before the
// stats domain reads from it.
//
// Filters mirror statsCountHandler: skip bots, anonymous admins, channel
// auto-forwards and service messages with no real `from`.
func membershipMessageMiddleware(svc *membership.Service, log *slog.Logger) th.Handler {
	return func(ctx *th.Context, update telego.Update) error {
		msg := update.Message
		if msg != nil && msg.From != nil && !msg.From.IsBot &&
			!shared.IsAnonymousAdmin(msg.From.ID) &&
			msg.SenderChat == nil && hasContent(msg) {
			ts := time.Unix(int64(msg.Date), 0).UTC()
			absChatID := storage.AbsChatID(msg.Chat.ID)
			if err := svc.RecordMessage(ctx.Context(), absChatID, msg.From, ts); err != nil {
				log.Error("membership.RecordMessage", "error", err, "chat_id", absChatID, "user_id", msg.From.ID)
			}
		}
		return ctx.Next(update)
	}
}

func membershipReactionHandler(svc *membership.Service, log *slog.Logger) th.MessageReactionHandler {
	return func(ctx *th.Context, reaction telego.MessageReactionUpdated) error {
		if err := svc.RecordReaction(ctx.Context(), reaction); err != nil {
			log.Error("membership.RecordReaction", "error", err, "chat_id", reaction.Chat.ID)
		}
		return nil
	}
}

func membershipChatMemberHandler(svc *membership.Service, adminCache *shared.AdminCache, log *slog.Logger) th.ChatMemberUpdatedHandler {
	return func(ctx *th.Context, cmu telego.ChatMemberUpdated) error {
		if err := svc.RecordChatMember(ctx.Context(), cmu); err != nil {
			log.Error("membership.RecordChatMember", "error", err, "chat_id", cmu.Chat.ID)
		}
		// any change in chat membership can affect admin set - invalidate cache
		adminCache.Invalidate(storage.AbsChatID(cmu.Chat.ID))
		return nil
	}
}

func membershipMyChatMemberHandler(svc *membership.Service, app *App, log *slog.Logger) th.ChatMemberUpdatedHandler {
	return func(ctx *th.Context, cmu telego.ChatMemberUpdated) error {
		if err := svc.RecordMyChatMember(ctx.Context(), cmu); err != nil {
			log.Error("membership.RecordMyChatMember", "error", err, "chat_id", cmu.Chat.ID)
		}
		newStatus := cmu.NewChatMember.MemberStatus()
		bgCtx := context.Background()

		if cmu.Chat.Type != telego.ChatTypeSupergroup {
			if cmu.Chat.Type == telego.ChatTypeGroup {
				_, _ = app.sender.SendMessage(bgCtx, &telego.SendMessageParams{
					ChatID: telego.ChatID{ID: cmu.Chat.ID},
					Text:   text.MsgNotSupergroup,
				})
			}
			return nil
		}

		switch newStatus {
		case "administrator":
			app.adminCache.Invalidate(storage.AbsChatID(cmu.Chat.ID))
			// One-time discoverability cue. Without this the bot joins
			// a 200-person chat silently and nobody knows it exists or
			// that management happens privately. Single concise public
			// message - not moderation, so it does not break the
			// "no public management" principle.
			oldStatus := cmu.OldChatMember.MemberStatus()
			if oldStatus != "administrator" {
				_, _ = app.sender.SendMessage(bgCtx, &telego.SendMessageParams{
					ChatID:    telego.ChatID{ID: cmu.Chat.ID},
					Text:      msgOnboardingAdmin,
					ParseMode: telego.ModeHTML,
				})
			}
		case "member":
			_, _ = app.sender.SendMessage(bgCtx, &telego.SendMessageParams{
				ChatID: telego.ChatID{ID: cmu.Chat.ID},
				Text:   text.MsgNeedAdmin,
			})
		}
		return nil
	}
}
