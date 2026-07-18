package bot

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"math/rand/v2"
	"strings"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/domain/reputation"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
)

var errReputationTarget = errors.New("reputation target missing")

type reputationSender interface {
	SendMessage(context.Context, *telego.SendMessageParams) (*telego.Message, error)
	GetChatMember(context.Context, *telego.GetChatMemberParams) (telego.ChatMember, error)
}

type reputationMembers interface {
	GetMemberByUsername(context.Context, int64, string) (*membership.Member, error)
	GetMember(context.Context, int64, int64) (*membership.Member, error)
}

// ReputationHandler makes praise, roast, and score commands durable per-chat.
type ReputationHandler struct {
	bot     reputationSender
	store   reputation.Store
	members reputationMembers
	admins  AdminChecker
	log     *slog.Logger
}

// NewReputationHandler constructs the public social-rating command handler.
// admins is optional for focused tests; production passes the shared admin cache.
func NewReputationHandler(bot reputationSender, store reputation.Store, members reputationMembers, log *slog.Logger, admins ...AdminChecker) *ReputationHandler {
	if log == nil {
		log = slog.Default()
	}
	h := &ReputationHandler{bot: bot, store: store, members: members, log: log}
	if len(admins) > 0 {
		h.admins = admins[0]
	}
	return h
}

func (h *ReputationHandler) HandlePraise(_ *th.Context, msg telego.Message) error {
	return h.apply(msg, reputation.KindPraise, praiseTemplates)
}

func (h *ReputationHandler) HandleRoast(_ *th.Context, msg telego.Message) error {
	return h.apply(msg, reputation.KindRoast, roastTemplates)
}

func (h *ReputationHandler) HandleRep(_ *th.Context, msg telego.Message) error {
	if msg.From == nil || msg.From.IsBot {
		return nil
	}
	isAdmin, ok := h.isAdmin(msg, msg.From.ID)
	if !ok {
		return h.reply(msg, "что-то сломалось... балансы не менялись.")
	}
	balance, err := h.store.Balance(context.Background(), storage.AbsChatID(msg.Chat.ID), msg.From.ID, isAdmin)
	if err != nil {
		h.log.Warn("reputation balance failed", "error", err, "chat_id", msg.Chat.ID, "user_id", msg.From.ID)
		return h.reply(msg, "что-то сломалось... балансы не менялись.")
	}
	return h.reply(msg, fmt.Sprintf("у %s осталось <b>%d</b> репутации...", h.actorDisplay(msg.From), balance))
}

func (h *ReputationHandler) HandleRepTop(_ *th.Context, msg telego.Message) error {
	if msg.From == nil || msg.From.IsBot {
		return nil
	}
	entries, err := h.store.Leaderboard(context.Background(), storage.AbsChatID(msg.Chat.ID), 10)
	if err != nil {
		h.log.Warn("reputation leaderboard failed", "error", err, "chat_id", msg.Chat.ID)
		return h.reply(msg, "что-то сломалось... балансы не менялись.")
	}
	lines := make([]string, 0, len(entries))
	for i, entry := range entries {
		lines = append(lines, fmt.Sprintf("%d. %s — <b>%d</b>", i+1, h.display(msg.Chat.ID, entry.UserID), entry.Balance))
	}
	if len(lines) == 0 {
		return h.reply(msg, "тут пока пусто... как обычно.")
	}
	return h.reply(msg, "<b>у кого ещё что-то осталось...</b>\n"+strings.Join(lines, "\n"))
}

func (h *ReputationHandler) apply(msg telego.Message, kind reputation.Kind, templates []string) error {
	if msg.From == nil || msg.From.IsBot {
		return nil
	}
	target, err := h.target(msg)
	if err != nil {
		return h.reply(msg, "нужно ответить на сообщение или написать /"+reputationCommand(kind)+" @username...")
	}
	// Live Telegram membership check: an old replied-to message can
	// outlive a missed leave event, and persisted membership can lie.
	// Reject before any balance initialization or mutation.
	if !h.isStillMember(msg.Chat.ID, target.UserID) {
		return h.reply(msg, "его уже нет в чате... репутация тут больше не поможет.")
	}
	if msg.From.ID == target.UserID {
		return h.reply(msg, "себе нельзя... даже так.")
	}
	actorAdmin, ok := h.isAdmin(msg, msg.From.ID)
	if !ok {
		return h.reply(msg, "что-то сломалось... балансы не менялись.")
	}
	targetAdmin, ok := h.isAdmin(msg, target.UserID)
	if !ok {
		return h.reply(msg, "что-то сломалось... балансы не менялись.")
	}
	result, err := h.store.Apply(context.Background(), storage.AbsChatID(msg.Chat.ID), msg.From.ID, target.UserID, kind, actorAdmin, targetAdmin)
	if err != nil {
		return h.replyApplyError(msg, err)
	}
	body := strings.Replace(templates[rand.IntN(len(templates))], "%s", h.memberDisplay(target), 1)
	actorLabel := h.actorDisplay(msg.From)
	delta := "+3"
	if kind == reputation.KindPraise && targetAdmin {
		delta = "+6"
	}
	if kind == reputation.KindRoast {
		return h.reply(msg, fmt.Sprintf("%s\n\nлови -1, чучело, от %s.\n<i>(Баланс: %s: %d, %s: %d)</i>", body, actorLabel, actorLabel, result.ActorBalance, h.memberDisplay(target), result.TargetBalance))
	}
	return h.reply(msg, fmt.Sprintf("%s\n\nдержи %s от %s.\n<i>(Баланс: %s: %d, %s: %d)</i>", body, delta, actorLabel, actorLabel, result.ActorBalance, h.memberDisplay(target), result.TargetBalance))
}

func (h *ReputationHandler) target(msg telego.Message) (*membership.Member, error) {
	arg := strings.TrimSpace(commandArgs(msg.Text))
	if arg != "" {
		if i := strings.IndexFunc(arg, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' }); i >= 0 {
			arg = arg[:i]
		}
		arg = strings.TrimPrefix(arg, "@")
		if arg != "" {
			return h.members.GetMemberByUsername(context.Background(), storage.AbsChatID(msg.Chat.ID), arg)
		}
	}
	if msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil && !msg.ReplyToMessage.From.IsBot {
		user := msg.ReplyToMessage.From
		return &membership.Member{UserID: user.ID, Username: user.Username, FirstName: user.FirstName}, nil
	}
	return nil, errReputationTarget
}

func (h *ReputationHandler) isAdmin(msg telego.Message, userID int64) (bool, bool) {
	if h.admins == nil {
		return false, true
	}
	isAdmin, err := h.admins.IsAdmin(storage.AbsChatID(msg.Chat.ID), userID)
	if err != nil {
		h.log.Warn("reputation admin check failed", "error", err, "chat_id", msg.Chat.ID, "user_id", userID)
		return false, false
	}
	return isAdmin, true
}

func (h *ReputationHandler) replyApplyError(msg telego.Message, err error) error {
	switch {
	case errors.Is(err, reputation.ErrSelfTarget):
		return h.reply(msg, "себе нельзя... даже так.")
	case errors.Is(err, reputation.ErrInsufficientBalance):
		return h.reply(msg, "у тебя ничего не осталось... раздавать больше нечего.")
	case errors.Is(err, reputation.ErrTargetInsufficientBalance):
		return h.reply(msg, "у него уже ноль... ниже некуда.")
	default:
		h.log.Warn("reputation apply failed", "error", err, "chat_id", msg.Chat.ID, "user_id", msg.From.ID)
		return h.reply(msg, "что-то сломалось... балансы не менялись.")
	}
}

// isStillMember returns true when Telegram confirms the user is
// currently a chat member. Fail-closed on API error or nil result so
// an unreachable API never mutates a possibly-departed user's balance.
func (h *ReputationHandler) isStillMember(chatID, userID int64) bool {
	cm, err := h.bot.GetChatMember(context.Background(), &telego.GetChatMemberParams{
		ChatID: telego.ChatID{ID: chatID},
		UserID: userID,
	})
	if err != nil || cm == nil {
		h.log.Warn("reputation live membership check failed", "error", err, "chat_id", chatID, "user_id", userID)
		return false
	}
	return cm.MemberIsMember()
}

// actorDisplay renders the actor's inert display name with the same
// rules as memberDisplay: handle without '@', escaped for HTML, with
// numeric-ID fallback when neither username nor first name is known.
func (h *ReputationHandler) actorDisplay(from *telego.User) string {
	display := shared.UserDisplay(from.Username, from.FirstName)
	if display == "" {
		return fmt.Sprintf("%d", from.ID)
	}
	return html.EscapeString(display)
}

func (h *ReputationHandler) display(chatID, userID int64) string {
	member, err := h.members.GetMember(context.Background(), userID, storage.AbsChatID(chatID))
	if err == nil {
		return h.memberDisplay(member)
	}
	return fmt.Sprintf("%d", userID)
}

func (h *ReputationHandler) memberDisplay(member *membership.Member) string {
	display := shared.UserDisplay(member.Username, member.FirstName)
	if display == "" {
		display = fmt.Sprintf("%d", member.UserID)
	}
	return html.EscapeString(display)
}

func (h *ReputationHandler) reply(msg telego.Message, body string) error {
	_, err := h.bot.SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: msg.Chat.ID},
		Text:      body,
		ParseMode: telego.ModeHTML,
		ReplyParameters: &telego.ReplyParameters{
			MessageID: msg.MessageID,
		},
	})
	if err != nil {
		h.log.Warn("reputation reply failed", "error", err, "chat_id", msg.Chat.ID)
	}
	return err
}

func reputationCommand(kind reputation.Kind) string {
	if kind == reputation.KindPraise {
		return "praise"
	}
	return "roast"
}
