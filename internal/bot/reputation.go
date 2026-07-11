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
		return h.reply(msg, "⚠️ <code>error</code>")
	}
	balance, err := h.store.Balance(context.Background(), storage.AbsChatID(msg.Chat.ID), msg.From.ID, isAdmin)
	if err != nil {
		h.log.Warn("reputation balance failed", "error", err, "chat_id", msg.Chat.ID, "user_id", msg.From.ID)
		return h.reply(msg, "⚠️ <code>error</code>")
	}
	return h.reply(msg, fmt.Sprintf("<code>%d</code>", balance))
}

func (h *ReputationHandler) HandleRepTop(_ *th.Context, msg telego.Message) error {
	if msg.From == nil || msg.From.IsBot {
		return nil
	}
	entries, err := h.store.Leaderboard(context.Background(), storage.AbsChatID(msg.Chat.ID), 10)
	if err != nil {
		h.log.Warn("reputation leaderboard failed", "error", err, "chat_id", msg.Chat.ID)
		return h.reply(msg, "⚠️ <code>error</code>")
	}
	lines := make([]string, 0, len(entries))
	for i, entry := range entries {
		lines = append(lines, fmt.Sprintf("%d. %s: %d", i+1, h.display(msg.Chat.ID, entry.UserID), entry.Balance))
	}
	if len(lines) == 0 {
		return h.reply(msg, "<code>-</code>")
	}
	return h.reply(msg, "<code>"+strings.Join(lines, "\n")+"</code>")
}

func (h *ReputationHandler) apply(msg telego.Message, kind reputation.Kind, templates []string) error {
	if msg.From == nil || msg.From.IsBot {
		return nil
	}
	target, err := h.target(msg)
	if err != nil {
		return h.reply(msg, "⚠️ <code>/"+reputationCommand(kind)+" @username</code>")
	}
	actorAdmin, ok := h.isAdmin(msg, msg.From.ID)
	if !ok {
		return h.reply(msg, "⚠️ <code>error</code>")
	}
	targetAdmin, ok := h.isAdmin(msg, target.UserID)
	if !ok {
		return h.reply(msg, "⚠️ <code>error</code>")
	}
	result, err := h.store.Apply(context.Background(), storage.AbsChatID(msg.Chat.ID), msg.From.ID, target.UserID, kind, actorAdmin, targetAdmin)
	if err != nil {
		return h.replyApplyError(msg, err)
	}
	body := strings.Replace(templates[rand.IntN(len(templates))], "%s", h.memberDisplay(target), 1)
	return h.reply(msg, fmt.Sprintf("%s\n<code>%d -> %d</code>", body, result.ActorBalance, result.TargetBalance))
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
	code := "error"
	switch {
	case errors.Is(err, reputation.ErrSelfTarget):
		code = "self"
	case errors.Is(err, reputation.ErrInsufficientBalance):
		code = "balance"
	case errors.Is(err, reputation.ErrTargetInsufficientBalance):
		code = "target"
	default:
		h.log.Warn("reputation apply failed", "error", err, "chat_id", msg.Chat.ID, "user_id", msg.From.ID)
	}
	return h.reply(msg, "⚠️ <code>"+code+"</code>")
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
