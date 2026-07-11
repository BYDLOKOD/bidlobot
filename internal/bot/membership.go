package bot

import (
	"context"
	"fmt"
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

// msgOnboardingAdmin is the public message posted when the bot is
// promoted to administrator. It tells members what commands are available.
const msgOnboardingAdmin = "<b>BidloBot</b> подключён.\n\n" +
	"Статистика и игры: /stats, /dice, /quiz.\n" +
	"Администраторам: /summarize - итог чата через AI."

func membershipMyChatMemberHandler(svc *membership.Service, app *App, log *slog.Logger) th.ChatMemberUpdatedHandler {
	return func(ctx *th.Context, cmu telego.ChatMemberUpdated) error {
		// Owner-only admission check: must happen before any
		// RecordMyChatMember, onboarding, or public send.
		switch EvaluateMyChatMemberAdmission(cmu, app.botOwnerID) {
		case AdmissionReject:
			bgCtx := context.Background()
			leaveErr := app.leaver.LeaveChat(bgCtx, &telego.LeaveChatParams{
				ChatID: telego.ChatID{ID: cmu.Chat.ID},
			})
			log.Warn("non-owner add rejected",
				"chat_id", cmu.Chat.ID,
				"actor_id", cmu.From.ID,
				"leave_error", leaveErr,
			)
			if leaveErr == nil {
				_, err := app.sender.SendMessage(bgCtx, &telego.SendMessageParams{
					ChatID: telego.ChatID{ID: app.botOwnerID},
					Text:   fmt.Sprintf("Unauthorized bot admission rejected for chat %d.", cmu.Chat.ID),
				})
				if err != nil {
					log.Warn("owner admission notice failed", "error", err)
				}
			}
			return nil
		case AdmissionIgnore:
			// Not an add transition or not a supergroup;
			// proceed with normal membership tracking.
		case AdmissionAdmit:
			// Owner add: proceed with onboarding below.
		}

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
			// One-time discoverability cue.
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

// AdmissionDecision classifies a my_chat_member update for owner-only admission.
type AdmissionDecision int

const (
	// AdmissionIgnore means the update is not a supergroup add transition;
	// the handler should process it normally (promotion, demotion, etc.).
	AdmissionIgnore AdmissionDecision = iota

	// AdmissionAdmit means the owner added the bot; proceed to onboarding.
	AdmissionAdmit

	// AdmissionReject means a non-owner added the bot; leave silently.
	AdmissionReject
)

// EvaluateMyChatMemberAdmission determines whether a my_chat_member update
// should be admitted (owner added the bot), rejected (non-owner added it,
// should leave silently), or ignored (not an add transition or not a
// supergroup). This is a pure, wireable decision - the caller is
// responsible for executing LeaveChat, RecordMyChatMember, onboarding
// sends, and the owner DM notification.
func EvaluateMyChatMemberAdmission(cmu telego.ChatMemberUpdated, ownerID int64) AdmissionDecision {
	if cmu.Chat.Type != telego.ChatTypeSupergroup {
		return AdmissionIgnore
	}

	oldStatus := cmu.OldChatMember.MemberStatus()
	newStatus := cmu.NewChatMember.MemberStatus()

	// An add transition: the bot went from left/kicked to member/administrator.
	isAdd := (oldStatus == telego.MemberStatusLeft || oldStatus == telego.MemberStatusBanned) &&
		(newStatus == telego.MemberStatusMember || newStatus == telego.MemberStatusAdministrator)

	if !isAdd {
		return AdmissionIgnore
	}

	if cmu.From.ID == ownerID {
		return AdmissionAdmit
	}

	return AdmissionReject
}
