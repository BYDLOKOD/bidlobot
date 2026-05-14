package profile

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/veschin/bidlobot/internal/text"
)

type Handler struct {
	svc *Service
	bot *telego.Bot
	log *slog.Logger
}

func NewHandler(svc *Service, bot *telego.Bot, log *slog.Logger) *Handler {
	return &Handler{svc: svc, bot: bot, log: log}
}

func (h *Handler) HandleRegister(_ *th.Context, msg telego.Message) error {
	ctx := context.Background()
	userID := msg.From.ID
	chatID := msg.Chat.ID
	absChatID := absID(chatID)

	exists, err := h.svc.store.Exists(ctx, userID, absChatID)
	if err != nil {
		return err
	}
	if exists {
		return h.reply(ctx, chatID, text.ErrProfileExists)
	}

	botUser, err := h.bot.GetMe(ctx)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("https://t.me/%s?start=reg_%d", botUser.Username, absChatID)
	_, err = h.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID: tu.ID(chatID),
		Text:   text.MsgRegisterPrompt,
		ReplyMarkup: tu.InlineKeyboard(tu.InlineKeyboardRow(
			telego.InlineKeyboardButton{Text: "Register", URL: url},
		)),
	})
	return err
}

func (h *Handler) HandleProfile(_ *th.Context, msg telego.Message) error {
	ctx := context.Background()
	userID := msg.From.ID
	chatID := msg.Chat.ID
	absChatID := absID(chatID)

	args := strings.Fields(msg.Text)

	if len(args) == 1 {
		p, err := h.svc.Get(ctx, userID, absChatID)
		if err != nil {
			return h.reply(ctx, chatID, text.ErrNoOwnProfile)
		}
		return h.replyHTML(ctx, chatID, h.svc.FormatProfile(p))
	}

	arg := args[1]
	if strings.HasPrefix(arg, "@") {
		username := strings.TrimPrefix(arg, "@")
		p, err := h.svc.GetByUsername(ctx, absChatID, username)
		if err != nil {
			return h.reply(ctx, chatID, text.ErrUserNotFound)
		}
		return h.replyHTML(ctx, chatID, h.svc.FormatProfile(p))
	}

	targetID, err := strconv.ParseInt(arg, 10, 64)
	if err != nil {
		return h.reply(ctx, chatID, text.ErrInvalidArg)
	}

	p, err := h.svc.Get(ctx, targetID, absChatID)
	if err != nil {
		return h.reply(ctx, chatID, text.ErrUserNotFound)
	}
	return h.replyHTML(ctx, chatID, h.svc.FormatProfile(p))
}

func (h *Handler) HandleUpdate(_ *th.Context, msg telego.Message) error {
	ctx := context.Background()
	userID := msg.From.ID
	chatID := msg.Chat.ID
	absChatID := absID(chatID)

	parts := strings.Fields(msg.Text)
	if len(parts) == 1 {
		botUser, err := h.bot.GetMe(ctx)
		if err != nil {
			return err
		}
		url := fmt.Sprintf("https://t.me/%s?start=upd_%d", botUser.Username, absChatID)
		_, err = h.bot.SendMessage(ctx, &telego.SendMessageParams{
			ChatID: tu.ID(chatID),
			Text:   "To update your profile, continue in private messages",
			ReplyMarkup: tu.InlineKeyboard(tu.InlineKeyboardRow(
				telego.InlineKeyboardButton{Text: "Update", URL: url},
			)),
		})
		return err
	}

	field := parts[1]
	if len(parts) < 3 {
		return h.reply(ctx, chatID, fmt.Sprintf(text.ErrProvideValue, field))
	}

	value := strings.Join(parts[2:], " ")
	if strings.TrimSpace(value) == "" {
		return h.reply(ctx, chatID, fmt.Sprintf(text.ErrProvideValue, field))
	}

	prof, err := h.svc.Get(ctx, userID, absChatID)
	if err != nil {
		return h.reply(ctx, chatID, text.ErrProfileNotFound)
	}

	switch field {
	case "stack":
		prof.Stack = strings.Split(value, ", ")
	case "bio":
		prof.Bio = value
	case "location":
		parts := strings.Split(value, ", ")
		if len(parts) > 0 {
			prof.Location = &LocationInfo{
				City:     parts[0],
				Timezone: "",
			}
			if len(parts) > 1 {
				prof.Location.Timezone = parts[1]
			}
		}
	default:
		return h.reply(ctx, chatID, text.ErrUnknownField)
	}

	if err := h.svc.Update(ctx, prof); err != nil {
		return err
	}
	return h.reply(ctx, chatID, text.MsgProfileUpdated)
}

func (h *Handler) reply(ctx context.Context, chatID int64, s string) error {
	_, err := h.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID: tu.ID(chatID),
		Text:   s,
	})
	return err
}

func (h *Handler) replyHTML(ctx context.Context, chatID int64, s string) error {
	_, err := h.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:    tu.ID(chatID),
		Text:      s,
		ParseMode: "HTML",
	})
	return err
}

func (h *Handler) FSMSweeper(ctx context.Context) {
	h.svc.FSM().RunSweeper(ctx, 5*time.Minute)
}

func absID(chatID int64) int64 {
	if chatID < 0 {
		return -chatID
	}
	return chatID
}
