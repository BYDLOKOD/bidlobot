package stats

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/text"
)

// UsernameLookup resolves @username to user_id within a chat. The current
// stats handler tolerates a nil implementation (per-username lookups simply
// reply "not found"). A real implementation will be wired in Phase 1 when
// membership tracking lands.
type UsernameLookup interface {
	GetByUsername(ctx context.Context, absChatID int64, username string) (userID int64, err error)
}

type Handler struct {
	svc    *Service
	lookup UsernameLookup
	log    *slog.Logger
}

// NewHandler создаёт обработчик команды /stats.
func NewHandler(svc *Service, lookup UsernameLookup, log *slog.Logger) *Handler {
	return &Handler{
		svc:    svc,
		lookup: lookup,
		log:    log,
	}
}

// HandleStats обрабатывает команду /stats с подкомандами.
// Поддерживаемые форматы:
// - /stats - обзор чата
// - /stats top - топ-5 пользователей
// - /stats today - статистика за день
// - /stats @username - статистика по имени пользователя
// - /stats 123456 - статистика по ID пользователя
func (h *Handler) HandleStats(ctx *th.Context, msg telego.Message) error {
	if msg.Chat.Type != telego.ChatTypeSupergroup {
		return h.replyError(ctx, msg, text.ErrStatsGroupOnly)
	}

	parts := strings.Fields(msg.Text)
	if len(parts) < 2 {
		// Нет подкомаанды - показать обзор чата.
		return h.handleChatOverview(ctx, msg)
	}

	subcommand := strings.ToLower(parts[1])

	switch subcommand {
	case "top":
		return h.handleTop(ctx, msg)
	case "today":
		return h.handleToday(ctx, msg)
	default:
		if strings.HasPrefix(subcommand, "@") {
			// Поиск по имени пользователя.
			username := strings.TrimPrefix(subcommand, "@")
			return h.handleUserByUsername(ctx, msg, username)
		}

		if userID, err := strconv.ParseInt(subcommand, 10, 64); err == nil {
			// Поиск по ID.
			return h.handleUserByID(ctx, msg, userID)
		}

		// Неизвестная подкомаанда.
		return h.replyError(ctx, msg, text.ErrStatsUnknownSub)
	}
}

func (h *Handler) handleChatOverview(ctx *th.Context, msg telego.Message) error {
	bgCtx := context.Background()
	absChatID := msg.Chat.ID
	if absChatID < 0 {
		absChatID = -absChatID
	}

	text, err := h.svc.ChatOverview(bgCtx, absChatID)
	if err != nil {
		h.log.Error("chat overview failed", "error", err)
		return h.replyError(ctx, msg, "Failed to retrieve statistics.")
	}

	return h.replyHTML(ctx, msg, text)
}

func (h *Handler) handleTop(ctx *th.Context, msg telego.Message) error {
	bgCtx := context.Background()
	absChatID := msg.Chat.ID
	if absChatID < 0 {
		absChatID = -absChatID
	}

	text, err := h.svc.Top(bgCtx, absChatID)
	if err != nil {
		h.log.Error("top stats failed", "error", err)
		return h.replyError(ctx, msg, "Failed to retrieve top users.")
	}

	return h.replyHTML(ctx, msg, text)
}

func (h *Handler) handleToday(ctx *th.Context, msg telego.Message) error {
	bgCtx := context.Background()
	absChatID := msg.Chat.ID
	if absChatID < 0 {
		absChatID = -absChatID
	}

	text, err := h.svc.Today(bgCtx, absChatID)
	if err != nil {
		h.log.Error("today stats failed", "error", err)
		return h.replyError(ctx, msg, "Failed to retrieve today's statistics.")
	}

	return h.replyHTML(ctx, msg, text)
}

func (h *Handler) handleUserByID(ctx *th.Context, msg telego.Message, userID int64) error {
	bgCtx := context.Background()
	absChatID := msg.Chat.ID
	if absChatID < 0 {
		absChatID = -absChatID
	}

	statsText, err := h.svc.UserStats(bgCtx, absChatID, userID, "")
	if err != nil {
		return h.replyError(ctx, msg, text.ErrStatsUserNotFound)
	}

	return h.replyHTML(ctx, msg, statsText)
}

func (h *Handler) handleUserByUsername(ctx *th.Context, msg telego.Message, username string) error {
	bgCtx := context.Background()
	absChatID := msg.Chat.ID
	if absChatID < 0 {
		absChatID = -absChatID
	}

	if h.lookup == nil {
		return h.replyError(ctx, msg, text.ErrStatsUserNotFound)
	}

	userID, err := h.lookup.GetByUsername(bgCtx, absChatID, username)
	if err != nil {
		return h.replyError(ctx, msg, text.ErrStatsUserNotFound)
	}

	return h.handleUserByID(ctx, msg, userID)
}

func (h *Handler) replyError(ctx *th.Context, msg telego.Message, errText string) error {
	return h.replyText(ctx, msg, errText)
}

func (h *Handler) replyHTML(ctx *th.Context, msg telego.Message, htmlText string) error {
	return h.replyText(ctx, msg, htmlText)
}

func (h *Handler) replyText(ctx *th.Context, msg telego.Message, text string) error {
	params := &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: msg.Chat.ID},
		Text:   text,
		ReplyParameters: &telego.ReplyParameters{
			MessageID: msg.MessageID,
		},
		ParseMode: "HTML",
	}

	_, err := ctx.Bot().SendMessage(context.Background(), params)
	return err
}
