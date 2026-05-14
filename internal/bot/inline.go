package bot

import (
	"context"
	"crypto/sha1"
	"fmt"
	"log/slog"
	"strings"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

// inlineCommand describes one offer the bot suggests when the user types
// "@bidlobot ..." in any chat. The result fires a regular slash-command
// in the destination chat (Telegram inserts the result as a message from
// the user); subsequent handling uses the existing /stats / /warns /
// /help routes.
//
// Putting destructive moderation commands here is unsafe - inline-query
// results are public to everyone in the chat and there is no chat_id in
// the inline query, so admin checks have to be deferred. They will land
// in Phase 3 with a pending-actions storage and a confirm-callback path.
type inlineCommand struct {
	id          string // stable identifier inside an inline-query response
	title       string // shown in the inline carousel
	description string // shown one line below the title
	send        string // the slash-command text Telegram will insert into the chat
}

func catalog() []inlineCommand {
	return []inlineCommand{
		{
			id:          "stats",
			title:       "📊 Статистика чата",
			description: "Отправить /stats - общий обзор",
			send:        "/stats",
		},
		{
			id:          "stats_top",
			title:       "🏆 Топ участников",
			description: "Отправить /stats top - топ-5 по сообщениям",
			send:        "/stats top",
		},
		{
			id:          "stats_today",
			title:       "📅 Активность за сегодня",
			description: "Отправить /stats today",
			send:        "/stats today",
		},
		{
			id:          "help",
			title:       "❓ Помощь",
			description: "Отправить /help - список команд",
			send:        "/help",
		},
	}
}

// buildInlineResults dispatches the user's inline query into a list of
// telego inline results. The function is pure (no IO) so it can be unit
// tested without a Telegram client.
func buildInlineResults(query string) []telego.InlineQueryResult {
	q := strings.TrimSpace(query)
	if q == "" {
		return toResults(catalog())
	}

	parts := strings.Fields(q)
	cmd := strings.ToLower(parts[0])
	switch cmd {
	case "stats":
		return toResults(statsCommands(parts[1:]))
	case "warns":
		return toResults(warnsCommands(parts[1:]))
	case "help":
		return toResults(helpCommands())
	default:
		return toResults(filterByPrefix(catalog(), q))
	}
}

func statsCommands(args []string) []inlineCommand {
	if len(args) == 0 {
		return []inlineCommand{
			{id: "stats", title: "📊 /stats", description: "Обзор чата", send: "/stats"},
			{id: "stats_top", title: "🏆 /stats top", description: "Топ участников", send: "/stats top"},
			{id: "stats_today", title: "📅 /stats today", description: "Активность за сегодня", send: "/stats today"},
		}
	}
	first := args[0]
	switch strings.ToLower(first) {
	case "top":
		return []inlineCommand{{id: "stats_top", title: "🏆 /stats top", description: "Топ участников чата", send: "/stats top"}}
	case "today":
		return []inlineCommand{{id: "stats_today", title: "📅 /stats today", description: "Активность за текущий день", send: "/stats today"}}
	default:
		// stats @user или stats <id> - формируем команду из всего хвоста
		send := "/stats " + strings.Join(args, " ")
		return []inlineCommand{{
			id:          "stats_user_" + sha1Hex(send),
			title:       "👤 " + send,
			description: "Статистика конкретного пользователя",
			send:        send,
		}}
	}
}

func warnsCommands(args []string) []inlineCommand {
	if len(args) == 0 {
		return []inlineCommand{{
			id:          "warns_help",
			title:       "⚠️ /warns",
			description: "Укажите пользователя: warns @user",
			send:        "/warns",
		}}
	}
	send := "/warns " + strings.Join(args, " ")
	return []inlineCommand{{
		id:          "warns_view_" + sha1Hex(send),
		title:       "⚠️ " + send,
		description: "Посмотреть предупреждения пользователя",
		send:        send,
	}}
}

func helpCommands() []inlineCommand {
	return []inlineCommand{{id: "help", title: "❓ /help", description: "Список команд бота", send: "/help"}}
}

// filterByPrefix returns catalog entries whose send-text matches the
// query as a case-insensitive prefix. Lets the autocomplete carousel
// shrink as the user types ("@bidlobot st" -> stats* only).
func filterByPrefix(items []inlineCommand, query string) []inlineCommand {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return items
	}
	var out []inlineCommand
	for _, item := range items {
		hay := strings.ToLower(item.send + " " + item.title)
		if strings.Contains(hay, q) {
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		return items
	}
	return out
}

func toResults(cmds []inlineCommand) []telego.InlineQueryResult {
	results := make([]telego.InlineQueryResult, 0, len(cmds))
	for _, c := range cmds {
		results = append(results, &telego.InlineQueryResultArticle{
			Type:        telego.ResultTypeArticle,
			ID:          c.id,
			Title:       c.title,
			Description: c.description,
			InputMessageContent: &telego.InputTextMessageContent{
				MessageText: c.send,
			},
		})
	}
	return results
}

func sha1Hex(s string) string {
	h := sha1.Sum([]byte(s))
	return fmt.Sprintf("%x", h[:6])
}

// inlineQueryHandler answers every InlineQuery with cache_time=0 and
// is_personal=true so that future destructive commands (Phase 3) can
// safely be admin-gated without leaking cached results between users.
func inlineQueryHandler(log *slog.Logger) th.InlineQueryHandler {
	return func(ctx *th.Context, query telego.InlineQuery) error {
		results := buildInlineResults(query.Query)
		err := ctx.Bot().AnswerInlineQuery(context.Background(), &telego.AnswerInlineQueryParams{
			InlineQueryID: query.ID,
			Results:       results,
			CacheTime:     0,
			IsPersonal:    true,
		})
		if err != nil {
			log.Warn("AnswerInlineQuery failed", "error", err, "query", query.Query, "user_id", query.From.ID)
		}
		return nil
	}
}
