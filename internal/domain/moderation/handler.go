package moderation

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/text"
)

type MessageHandler func(ctx *th.Context, msg telego.Message) error

type UsernameLookup interface {
	GetByUsername(ctx context.Context, absChatID int64, username string) (userID int64, isBot bool, err error)
}

type Handler struct {
	svc    *Service
	admin  *shared.AdminCache
	lookup UsernameLookup
	log    *slog.Logger
}

func NewHandler(svc *Service, admin *shared.AdminCache, lookup UsernameLookup, log *slog.Logger) *Handler {
	return &Handler{
		svc:    svc,
		admin:  admin,
		lookup: lookup,
		log:    log,
	}
}

func (h *Handler) Service() *Service { return h.svc }

// HandleWarn обрабатывает команду /warn.
func (h *Handler) HandleWarn(ctx *th.Context, msg telego.Message) error {
	chatID := msg.Chat.ID
	absChatID := absID(chatID)
	callerID := msg.From.ID

	target, reason, err := shared.ResolveTarget(&msg)
	if err != nil {
		_, _ = ctx.Bot().SendMessage(context.Background(), &telego.SendMessageParams{
			ChatID:    telego.ChatID{ID: chatID},
			Text:      text.ErrNoTarget,
			ParseMode: telego.ModeHTML,
		})
		return nil
	}

	// Resolve @username to user_id
	var isBot bool
	if target.UserID == 0 && target.Username != "" && h.lookup != nil {
		uid, ib, lookupErr := h.lookup.GetByUsername(context.Background(), absChatID, target.Username)
		if lookupErr != nil {
			_, _ = ctx.Bot().SendMessage(context.Background(), &telego.SendMessageParams{
				ChatID:    telego.ChatID{ID: chatID},
				Text:      text.ErrStatsUserNotFound,
				ParseMode: telego.ModeHTML,
			})
			return nil
		}
		target.UserID = uid
		isBot = ib
	}

	if err := h.svc.ValidateTarget(context.Background(), absChatID, callerID, target.UserID, isBot, "warn"); err != nil {
		_, _ = ctx.Bot().SendMessage(context.Background(), &telego.SendMessageParams{
			ChatID:    telego.ChatID{ID: chatID},
			Text:      err.Error(),
			ParseMode: telego.ModeHTML,
		})
		return nil
	}

	count, err := h.svc.Warn(context.Background(), absChatID, target.UserID, callerID, reason)
	if err != nil {
		h.log.Error("warn failed", slog.Any("err", err), slog.Int64("target", target.UserID))
		return err
	}

	reply := fmt.Sprintf("⚠️ %s warned (%d/3)", target.DisplayName, count)

	if count == 3 {
		if err := h.svc.AutoMute(context.Background(), chatID, target.UserID); err != nil {
			h.log.Error("automute failed", slog.Any("err", err), slog.Int64("target", target.UserID))
		}
		reply += "\nAuto-mute activated for 24 hours."
	} else if count > 3 {
		reply = fmt.Sprintf("⚠️ %s warned (%d total). Auto-mute threshold already reached.", target.DisplayName, count)
	}

	_, _ = ctx.Bot().SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: chatID},
		Text:      reply,
		ParseMode: telego.ModeHTML,
	})
	return nil
}

// HandleWarns обрабатывает команду /warns для просмотра или очистки предупреждений.
// Форматы: /warns [target] - список предупреждений, /warns clear [target] - очистить (только админ).
func (h *Handler) HandleWarns(ctx *th.Context, msg telego.Message) error {
	args := strings.Fields(msg.Text)

	if len(args) >= 2 && args[1] == "clear" {
		return h.handleWarnsClear(ctx, msg)
	}

	// Public view flow
	chatID := msg.Chat.ID
	absChatID := absID(chatID)

	target, _, err := shared.ResolveTarget(&msg)
	if err != nil {
		_, _ = ctx.Bot().SendMessage(context.Background(), &telego.SendMessageParams{
			ChatID:    telego.ChatID{ID: chatID},
			Text:      text.ErrNoTarget,
			ParseMode: telego.ModeHTML,
		})
		return nil
	}

	// Resolve @username to user_id
	if target.UserID == 0 && target.Username != "" && h.lookup != nil {
		uid, _, lookupErr := h.lookup.GetByUsername(context.Background(), absChatID, target.Username)
		if lookupErr != nil {
			_, _ = ctx.Bot().SendMessage(context.Background(), &telego.SendMessageParams{
				ChatID:    telego.ChatID{ID: chatID},
				Text:      text.ErrStatsUserNotFound,
				ParseMode: telego.ModeHTML,
			})
			return nil
		}
		target.UserID = uid
	}

	list, err := h.svc.ListWarnings(context.Background(), target.UserID, absChatID)
	if err != nil {
		h.log.Error("list warnings failed", slog.Any("err", err), slog.Int64("target", target.UserID))
		return err
	}

	_, _ = ctx.Bot().SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: chatID},
		Text:      list,
		ParseMode: telego.ModeHTML,
	})
	return nil
}

// handleWarnsClear очищает предупреждения для пользователя (admin-only).
// Формат: /warns clear @username или /warns clear как reply.
func (h *Handler) handleWarnsClear(ctx *th.Context, msg telego.Message) error {
	chatID := msg.Chat.ID
	absChatID := absID(chatID)

	if msg.From == nil {
		return nil
	}

	isAdmin, err := h.admin.IsAdmin(absChatID, msg.From.ID)
	if err != nil || !isAdmin {
		_, _ = ctx.Bot().SendMessage(context.Background(), &telego.SendMessageParams{
			ChatID: telego.ChatID{ID: chatID},
			Text:   text.ErrNotAdmin,
		})
		return nil
	}

	// /warns clear @bob -> args = ["clear", "@bob"]. Target is after "clear".
	// Parse manually: strip "/warns clear" prefix, then resolve rest.
	args := strings.Fields(msg.Text)
	var target shared.Target
	if msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil {
		from := msg.ReplyToMessage.From
		target = shared.Target{UserID: from.ID, Username: from.Username, DisplayName: shared.DisplayNameOf(from)}
	} else if len(args) >= 3 {
		arg := args[2]
		if strings.HasPrefix(arg, "@") {
			target.Username = strings.TrimPrefix(arg, "@")
			target.DisplayName = arg
		} else if uid, parseErr := strconv.ParseInt(arg, 10, 64); parseErr == nil {
			target.UserID = uid
			target.DisplayName = arg
		}
	}

	if target.UserID == 0 && target.Username == "" {
		_, _ = ctx.Bot().SendMessage(context.Background(), &telego.SendMessageParams{
			ChatID: telego.ChatID{ID: chatID},
			Text:   text.ErrNoTarget,
		})
		return nil
	}

	if target.UserID == 0 && target.Username != "" && h.lookup != nil {
		uid, _, lookupErr := h.lookup.GetByUsername(context.Background(), absChatID, target.Username)
		if lookupErr != nil {
			_, _ = ctx.Bot().SendMessage(context.Background(), &telego.SendMessageParams{
				ChatID: telego.ChatID{ID: chatID},
				Text:   text.ErrStatsUserNotFound,
			})
			return nil
		}
		target.UserID = uid
	}

	if err := h.svc.ClearWarnings(context.Background(), target.UserID, absChatID); err != nil {
		h.log.Error("clear warnings failed", slog.Any("err", err), slog.Int64("target", target.UserID))
		return err
	}

	reply := fmt.Sprintf(text.MsgWarningsCleared, target.DisplayName)
	_, _ = ctx.Bot().SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: chatID},
		Text:   reply,
	})
	return nil
}

// HandleMute обрабатывает команду /mute.
func (h *Handler) HandleMute(ctx *th.Context, msg telego.Message) error {
	chatID := msg.Chat.ID
	absChatID := absID(chatID)
	callerID := msg.From.ID

	target, args, err := shared.ResolveTarget(&msg)
	if err != nil {
		_, _ = ctx.Bot().SendMessage(context.Background(), &telego.SendMessageParams{
			ChatID:    telego.ChatID{ID: chatID},
			Text:      text.ErrNoTarget,
			ParseMode: telego.ModeHTML,
		})
		return nil
	}

	// Resolve @username to user_id
	var isBot bool
	if target.UserID == 0 && target.Username != "" && h.lookup != nil {
		uid, ib, lookupErr := h.lookup.GetByUsername(context.Background(), absChatID, target.Username)
		if lookupErr != nil {
			_, _ = ctx.Bot().SendMessage(context.Background(), &telego.SendMessageParams{
				ChatID:    telego.ChatID{ID: chatID},
				Text:      text.ErrStatsUserNotFound,
				ParseMode: telego.ModeHTML,
			})
			return nil
		}
		target.UserID = uid
		isBot = ib
	}

	duration := 1 * time.Hour
	if args != "" {
		parts := strings.Fields(args)
		if len(parts) > 0 {
			d, parseErr := parseDuration(parts[0])
			if parseErr != nil {
				_, _ = ctx.Bot().SendMessage(context.Background(), &telego.SendMessageParams{
					ChatID:    telego.ChatID{ID: chatID},
					Text:      text.ErrInvalidDuration,
					ParseMode: telego.ModeHTML,
				})
				return nil
			}
			duration = d
		}
	}

	if err := h.svc.ValidateTarget(context.Background(), absChatID, callerID, target.UserID, isBot, "mute"); err != nil {
		_, _ = ctx.Bot().SendMessage(context.Background(), &telego.SendMessageParams{
			ChatID:    telego.ChatID{ID: chatID},
			Text:      err.Error(),
			ParseMode: telego.ModeHTML,
		})
		return nil
	}

	if err := h.svc.Mute(context.Background(), chatID, target.UserID, duration); err != nil {
		h.log.Error("mute failed", slog.Any("err", err), slog.Int64("target", target.UserID))
		return err
	}

	reply := fmt.Sprintf("%s muted for %s.", target.DisplayName, duration.String())
	_, _ = ctx.Bot().SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: chatID},
		Text:      reply,
		ParseMode: telego.ModeHTML,
	})
	return nil
}

// HandleUnmute обрабатывает команду /unmute.
func (h *Handler) HandleUnmute(ctx *th.Context, msg telego.Message) error {
	chatID := msg.Chat.ID
	absChatID := absID(chatID)

	target, _, err := shared.ResolveTarget(&msg)
	if err != nil {
		_, _ = ctx.Bot().SendMessage(context.Background(), &telego.SendMessageParams{
			ChatID:    telego.ChatID{ID: chatID},
			Text:      text.ErrNoTarget,
			ParseMode: telego.ModeHTML,
		})
		return nil
	}

	// Resolve @username to user_id
	if target.UserID == 0 && target.Username != "" && h.lookup != nil {
		uid, _, lookupErr := h.lookup.GetByUsername(context.Background(), absChatID, target.Username)
		if lookupErr != nil {
			_, _ = ctx.Bot().SendMessage(context.Background(), &telego.SendMessageParams{
				ChatID:    telego.ChatID{ID: chatID},
				Text:      text.ErrStatsUserNotFound,
				ParseMode: telego.ModeHTML,
			})
			return nil
		}
		target.UserID = uid
	}

	if err := h.svc.Unmute(context.Background(), chatID, target.UserID); err != nil {
		h.log.Error("unmute failed", slog.Any("err", err), slog.Int64("target", target.UserID))
		return err
	}

	reply := fmt.Sprintf("%s unmuted.", target.DisplayName)
	_, _ = ctx.Bot().SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: chatID},
		Text:      reply,
		ParseMode: telego.ModeHTML,
	})
	return nil
}

// HandleBan обрабатывает команду /ban.
func (h *Handler) HandleBan(ctx *th.Context, msg telego.Message) error {
	chatID := msg.Chat.ID
	absChatID := absID(chatID)
	callerID := msg.From.ID

	target, _, err := shared.ResolveTarget(&msg)
	if err != nil {
		_, _ = ctx.Bot().SendMessage(context.Background(), &telego.SendMessageParams{
			ChatID:    telego.ChatID{ID: chatID},
			Text:      text.ErrNoTarget,
			ParseMode: telego.ModeHTML,
		})
		return nil
	}

	// Resolve @username to user_id
	var isBot bool
	if target.UserID == 0 && target.Username != "" && h.lookup != nil {
		uid, ib, lookupErr := h.lookup.GetByUsername(context.Background(), absChatID, target.Username)
		if lookupErr != nil {
			_, _ = ctx.Bot().SendMessage(context.Background(), &telego.SendMessageParams{
				ChatID:    telego.ChatID{ID: chatID},
				Text:      text.ErrStatsUserNotFound,
				ParseMode: telego.ModeHTML,
			})
			return nil
		}
		target.UserID = uid
		isBot = ib
	}

	if err := h.svc.ValidateTarget(context.Background(), absChatID, callerID, target.UserID, isBot, "ban"); err != nil {
		_, _ = ctx.Bot().SendMessage(context.Background(), &telego.SendMessageParams{
			ChatID:    telego.ChatID{ID: chatID},
			Text:      err.Error(),
			ParseMode: telego.ModeHTML,
		})
		return nil
	}

	if err := h.svc.Ban(context.Background(), chatID, target.UserID); err != nil {
		h.log.Error("ban failed", slog.Any("err", err), slog.Int64("target", target.UserID))
		return err
	}

	reply := fmt.Sprintf("%s banned.", target.DisplayName)
	_, _ = ctx.Bot().SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: chatID},
		Text:      reply,
		ParseMode: telego.ModeHTML,
	})
	return nil
}

// HandleUnban обрабатывает команду /unban.
func (h *Handler) HandleUnban(ctx *th.Context, msg telego.Message) error {
	chatID := msg.Chat.ID
	absChatID := absID(chatID)

	target, _, err := shared.ResolveTarget(&msg)
	if err != nil {
		_, _ = ctx.Bot().SendMessage(context.Background(), &telego.SendMessageParams{
			ChatID:    telego.ChatID{ID: chatID},
			Text:      text.ErrNoTarget,
			ParseMode: telego.ModeHTML,
		})
		return nil
	}

	// Resolve @username to user_id
	if target.UserID == 0 && target.Username != "" && h.lookup != nil {
		uid, _, lookupErr := h.lookup.GetByUsername(context.Background(), absChatID, target.Username)
		if lookupErr != nil {
			_, _ = ctx.Bot().SendMessage(context.Background(), &telego.SendMessageParams{
				ChatID:    telego.ChatID{ID: chatID},
				Text:      text.ErrStatsUserNotFound,
				ParseMode: telego.ModeHTML,
			})
			return nil
		}
		target.UserID = uid
	}

	if err := h.svc.Unban(context.Background(), chatID, target.UserID); err != nil {
		h.log.Error("unban failed", slog.Any("err", err), slog.Int64("target", target.UserID))
		return err
	}

	reply := fmt.Sprintf("%s unbanned.", target.DisplayName)
	_, _ = ctx.Bot().SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: chatID},
		Text:      reply,
		ParseMode: telego.ModeHTML,
	})
	return nil
}

// absID преобразует ID чата в абсолютное значение (отрицательное в положительное).
func absID(chatID int64) int64 {
	if chatID < 0 {
		return -chatID
	}
	return chatID
}

// parseDuration парсит строку длительности в time.Duration.
// Поддерживает: "30m", "1h", "2h", "12h", "1d", "7d", "30d".
// Минимум 1m, максимум 366d.
func parseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("%s", text.ErrInvalidDuration)
	}

	// Попытка прямого парсинга time.Duration
	d, err := time.ParseDuration(s)
	if err == nil {
		if d < 1*time.Minute || d > 366*24*time.Hour {
			return 0, fmt.Errorf("%s", text.ErrInvalidDuration)
		}
		return d, nil
	}

	// Кастомный парсинг для дней
	if strings.HasSuffix(s, "d") {
		num := strings.TrimSuffix(s, "d")
		n, parseErr := strconv.Atoi(num)
		if parseErr != nil {
			return 0, fmt.Errorf("%s", text.ErrInvalidDuration)
		}
		d := time.Duration(n) * 24 * time.Hour
		if d < 1*time.Minute || d > 366*24*time.Hour {
			return 0, fmt.Errorf("%s", text.ErrInvalidDuration)
		}
		return d, nil
	}

	return 0, fmt.Errorf("%s", text.ErrInvalidDuration)
}
