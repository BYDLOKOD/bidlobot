package bot

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
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

// ReputationHandler makes praise, roast, score and leaderboard commands
// durable per-chat. Administrators receive the same deltas as everyone
// else; the optional AdminChecker argument is kept for wiring stability.
type ReputationHandler struct {
	bot     reputationSender
	store   reputation.Store
	members reputationMembers
	log     *slog.Logger
}

// NewReputationHandler constructs the public social-rating command handler.
func NewReputationHandler(bot reputationSender, store reputation.Store, members reputationMembers, log *slog.Logger, _ ...AdminChecker) *ReputationHandler {
	if log == nil {
		log = slog.Default()
	}
	return &ReputationHandler{bot: bot, store: store, members: members, log: log}
}

func (h *ReputationHandler) HandlePraise(_ *th.Context, msg telego.Message) error {
	return h.apply(msg, reputation.KindPraise)
}

func (h *ReputationHandler) HandleRoast(_ *th.Context, msg telego.Message) error {
	return h.apply(msg, reputation.KindRoast)
}

func (h *ReputationHandler) HandleRep(_ *th.Context, msg telego.Message) error {
	if msg.From == nil || msg.From.IsBot {
		return nil
	}
	account, err := h.store.Wallet(context.Background(), msg.Chat.ID, msg.From.ID)
	if err != nil {
		h.log.Warn("reputation wallet failed", "error", err, "chat_id", msg.Chat.ID, "user_id", msg.From.ID)
		return h.reply(msg, "что-то сломалось... балансы не менялись.")
	}

	title := reputation.TitleForScore(account.Score)
	lines := []string{
		fmt.Sprintf("<b>%s</b>", h.actorDisplay(msg.From)),
		fmt.Sprintf("доступно: <b>%d</b>, в резерве: <b>%d</b>", account.Available, account.Reserved),
		fmt.Sprintf("счёт: <b>%d</b> \u2014 %s", account.Score, title.Name),
	}
	if next, ok := reputation.NextTitle(account.Score); ok {
		lines = append(lines, fmt.Sprintf("до \u00ab%s\u00bb: <b>%d</b>", next.Name, next.Threshold-account.Score))
	} else {
		lines = append(lines, "это высший титул")
	}
	return h.reply(msg, strings.Join(lines, "\n"))
}

func (h *ReputationHandler) HandleRepTop(_ *th.Context, msg telego.Message) error {
	if msg.From == nil || msg.From.IsBot {
		return nil
	}
	entries, err := h.store.Leaderboard(context.Background(), msg.Chat.ID, 0)
	if err != nil {
		h.log.Warn("reputation leaderboard failed", "error", err, "chat_id", msg.Chat.ID)
		return h.reply(msg, "что-то сломалось... балансы не менялись.")
	}

	lines := make([]string, 0, 10)
	for _, entry := range entries {
		if len(lines) == 10 {
			break
		}
		member := h.observedMember(msg.Chat.ID, entry.UserID)
		if member == nil || member.IsBot || !memberCurrent(member) {
			continue
		}
		title := reputation.TitleForScore(entry.Score)
		lines = append(lines, fmt.Sprintf("%d. %s \u2014 <b>%d</b>, %s · %s",
			len(lines)+1, h.memberDisplay(member), entry.Score, title.Name, title.Activity))
	}
	if len(lines) == 0 {
		return h.reply(msg, "пока никто не набрал счёт...")
	}
	return h.reply(msg, "<b>верх по счёту</b>\n"+strings.Join(lines, "\n"))
}

// observedMember resolves a member from the persisted membership record.
// The leaderboard is a read view and does not call Telegram.
func (h *ReputationHandler) observedMember(chatID, userID int64) *membership.Member {
	member, err := h.members.GetMember(context.Background(), userID, storage.AbsChatID(chatID))
	if err != nil {
		return nil
	}
	return member
}

// memberCurrent reports whether an observed membership is still current
// without a live API call. Live verification is reserved for writes.
func memberCurrent(member *membership.Member) bool {
	switch member.Status {
	case membership.StatusLeft, membership.StatusKicked:
		return false
	default:
		return true
	}
}

func (h *ReputationHandler) apply(msg telego.Message, kind reputation.Kind) error {
	if msg.From == nil || msg.From.IsBot {
		return nil
	}

	args := repCommandArgs(msg.Text)
	reason := repReasonNone
	if kind == reputation.KindPraise {
		reason, args = parseRepReason(args)
	}

	target, err := h.target(msg, args)
	if err != nil {
		return h.reply(msg, "нужно ответить на сообщение или написать /"+reputationCommand(kind)+" @username...")
	}
	if target.IsBot {
		return h.reply(msg, "ботам репутация не нужна...")
	}
	if msg.From.ID == target.UserID {
		return h.reply(msg, "себе нельзя... даже так.")
	}

	// A write requires live membership: an old replied-to message can
	// outlive a missed leave event, and persisted membership can lie.
	if !h.isStillMember(msg.Chat.ID, msg.From.ID) {
		return h.reply(msg, "не удалось проверить твоё участие... балансы не менялись.")
	}
	if !h.isStillMember(msg.Chat.ID, target.UserID) {
		return h.reply(msg, "его уже нет в чате... репутация тут больше не поможет.")
	}

	result, err := h.store.Settle(context.Background(), reputation.SocialRequest{
		ChatID:          msg.Chat.ID,
		ActorID:         msg.From.ID,
		TargetID:        target.UserID,
		Kind:            kind,
		Source:          reputation.SourceSlash,
		SourceMessageID: msg.MessageID,
	})
	if err != nil {
		return h.replyApplyError(msg, err)
	}

	actorLabel := h.actorDisplay(msg.From)
	targetLabel := h.memberDisplay(target)
	if kind == reputation.KindPraise {
		line := praiseLine(msg.Chat.ID, reason)
		return h.reply(msg, praiseReceipt(line, actorLabel, targetLabel, result))
	}
	return h.reply(msg, roastReceipt(actorLabel, targetLabel, result))
}

func praiseReceipt(line, actor, target string, result reputation.Result) string {
	if line == "" {
		line = "Плюс по адресу."
	}
	return fmt.Sprintf("%s\n\n<b>%+d</b> к репе %s, <b>%+d</b> к счёту; <b>%+d</b> у %s.\n<i>Доступно: %s \u2014 %d, %s \u2014 %d. Счёт: %s \u2014 %d.</i>",
		line,
		result.TargetWalletDelta, target,
		result.TargetScoreDelta,
		result.ActorWalletDelta, actor,
		actor, result.Actor.Available,
		target, result.Target.Available,
		target, result.Target.Score,
	)
}

func roastReceipt(actor, target string, result reputation.Result) string {
	return fmt.Sprintf("Минус по адресу.\n\n<b>%+d</b> к репе %s, <b>%+d</b> к счёту; <b>%+d</b> у %s.\n<i>Доступно: %s \u2014 %d, %s \u2014 %d. Счёт: %s \u2014 %d.</i>",
		result.TargetWalletDelta, target,
		result.TargetScoreDelta,
		result.ActorWalletDelta, actor,
		actor, result.Actor.Available,
		target, result.Target.Available,
		target, result.Target.Score,
	)
}

// target resolves the explicit reason-stripped argument or the replied-to
// human. Replies are never taken from bots.
func (h *ReputationHandler) target(msg telego.Message, args string) (*membership.Member, error) {
	arg := strings.TrimSpace(args)
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
		return &membership.Member{UserID: user.ID, Username: user.Username, FirstName: user.FirstName, IsBot: user.IsBot}, nil
	}
	return nil, errReputationTarget
}

func (h *ReputationHandler) replyApplyError(msg telego.Message, err error) error {
	switch {
	case errors.Is(err, reputation.ErrSelfTarget):
		return h.reply(msg, "себе нельзя... даже так.")
	case errors.Is(err, reputation.ErrInsufficientBalance):
		return h.reply(msg, "у тебя ничего не осталось... раздавать больше нечего.")
	case errors.Is(err, reputation.ErrPairLimit):
		return h.reply(msg, "с этой парой уже считались сегодня... до следующих суток.")
	case errors.Is(err, reputation.ErrSenderLimit):
		return h.reply(msg, "три пересчёта в сутки... лимит исчерпан.")
	case errors.Is(err, reputation.ErrDuplicateSource):
		return h.reply(msg, "этот плюс уже учтён... повторно не спишется.")
	default:
		h.log.Warn("reputation settle failed", "error", err, "chat_id", msg.Chat.ID, "user_id", msg.From.ID)
		return h.reply(msg, "что-то сломалось... балансы не менялись.")
	}
}

// isStillMember returns true when Telegram confirms the user is
// currently a chat member. Fail-closed on API error or nil result so an
// unreachable API never mutates a possibly-departed user's balance.
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

// actorDisplay renders the actor's inert display name: handle without
// '@', escaped for HTML, with numeric-ID fallback.
func (h *ReputationHandler) actorDisplay(from *telego.User) string {
	display := shared.UserDisplay(from.Username, from.FirstName)
	if display == "" {
		return fmt.Sprintf("%d", from.ID)
	}
	return html.EscapeString(display)
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

// repCommandArgs strips the slash command token (including a @botname
// suffix) and returns the remaining arguments.
func repCommandArgs(text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return text
	}
	if i := strings.IndexAny(text, " \t\n"); i >= 0 {
		return strings.TrimSpace(text[i+1:])
	}
	return ""
}
