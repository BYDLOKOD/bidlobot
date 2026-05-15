package bot

import (
	"log/slog"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/monthstats"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
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

// MonthlyIncrementer is the monthstats live sink. nil-safe: a bot built
// without the monthly engine still tracks lifetime stats.
type MonthlyIncrementer interface {
	Add(s monthstats.Sample)
}

func statsCountHandler(buffer StatsIncrementer, monthly MonthlyIncrementer) th.Handler {
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
			if monthly != nil {
				if s, ok := monthstats.ExtractSample(msg); ok {
					monthly.Add(s)
				}
			}
		}
		return ctx.Next(update)
	}
}

// hasContent delegates to monthstats.HasContent so the live exclusion
// predicate has exactly one definition shared with the importer.
func hasContent(msg *telego.Message) bool {
	return monthstats.HasContent(msg)
}
