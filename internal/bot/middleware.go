package bot

import (
	"context"
	"log/slog"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
	"github.com/veschin/bidlobot/internal/text"
)

func loggingHandler(log *slog.Logger) th.Handler {
	return func(ctx *th.Context, update telego.Update) error {
		start := time.Now()
		err := ctx.Next(update)
		dur := time.Since(start)

		attrs := []any{"duration_ms", dur.Milliseconds()}
		if msg := update.Message; msg != nil {
			attrs = append(attrs, "chat_id", msg.Chat.ID)
			if msg.From != nil {
				attrs = append(attrs, "user_id", msg.From.ID)
			}
		}
		if err != nil {
			attrs = append(attrs, "error", err)
			log.Error("handler error", attrs...)
		} else if dur > 100*time.Millisecond {
			log.Warn("slow handler", attrs...)
		}
		return err
	}
}

type StatsIncrementer interface {
	Increment(userID, absChatID int64, ts time.Time)
}

func statsCountHandler(buffer StatsIncrementer) th.Handler {
	return func(ctx *th.Context, update telego.Update) error {
		msg := update.Message
		if msg != nil && msg.From != nil && !msg.From.IsBot &&
			!shared.IsAnonymousAdmin(msg.From.ID) &&
			msg.SenderChat == nil && hasContent(msg) {
			buffer.Increment(
				msg.From.ID,
				storage.AbsChatID(msg.Chat.ID),
				time.Unix(int64(msg.Date), 0),
			)
		}
		return ctx.Next(update)
	}
}

func adminCheckHandler(cache *shared.AdminCache, tgBot *telego.Bot) th.Handler {
	return func(ctx *th.Context, update telego.Update) error {
		msg := update.Message
		if msg == nil || msg.From == nil {
			return nil
		}

		// Check for anonymous admin first
		if shared.IsAnonymousAdmin(msg.From.ID) {
			return replyText(ctx, tgBot, msg, text.ErrAnonymousAdmin)
		}

		absChatID := storage.AbsChatID(msg.Chat.ID)

		isAdmin, err := cache.IsAdmin(absChatID, msg.From.ID)
		if err != nil {
			return replyText(ctx, tgBot, msg, text.ErrBotLostRights)
		}
		if !isAdmin {
			return replyText(ctx, tgBot, msg, text.ErrNotAdmin)
		}

		canRestrict, err := cache.BotCanRestrict(absChatID)
		if err != nil || !canRestrict {
			return replyText(ctx, tgBot, msg, text.ErrBotNoRestrict)
		}

		return ctx.Next(update)
	}
}

func replyText(_ *th.Context, bot *telego.Bot, msg *telego.Message, s string) error {
	_, err := bot.SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: msg.Chat.ID},
		Text:   s,
		ReplyParameters: &telego.ReplyParameters{
			MessageID: msg.MessageID,
		},
	})
	return err
}

func hasContent(msg *telego.Message) bool {
	return msg.Text != "" || msg.Photo != nil || msg.Video != nil ||
		msg.Document != nil || msg.Sticker != nil || msg.Voice != nil ||
		msg.VideoNote != nil || msg.Audio != nil || msg.Animation != nil ||
		msg.Poll != nil || msg.Location != nil || msg.Contact != nil
}
