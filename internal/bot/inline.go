package bot

import (
	"context"
	"crypto/sha1"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

// InlineService backs HandleInlineQuery. All inline queries are read-only:
// stats variants and help are matched explicitly; everything else falls
// back to a prefix filter over the catalog.
type InlineService struct {
	log *slog.Logger
	// gameRouter is consulted before the default catalog filter so
	// mini-game commands (dice/battle/quiz) can register without
	// the inline package importing the games packages. Returns
	// (results, true) when the command was handled; (nil, false) when
	// the inline service should continue with the default filter.
	gameRouter InlineGameRouter
}

// InlineGameRouter is the contract a mini-game module implements to plug
// itself into inline-mode dispatch. The router receives the lowercased
// first token (cmd) and the trailing tokens (args). Implementations must
// be cheap and side-effect-free: inline queries fire on every keystroke.
type InlineGameRouter interface {
	Route(cmd string, args []string, actor telego.User) (results []telego.InlineQueryResult, handled bool)
}

func NewInlineService(log *slog.Logger) *InlineService {
	return &InlineService{log: log}
}

// SetGameRouter wires a router that handles dice/battle/quiz inline
// queries. Idempotent; passing nil disables routing.
func (s *InlineService) SetGameRouter(r InlineGameRouter) { s.gameRouter = r }

// inlineCommand describes one offer the bot suggests when the user types
// "@bidlobot ..." in any chat. All entries are read-only and fire a
// regular slash-command message in the destination chat.
type inlineCommand struct {
	id          string // stable identifier inside an inline-query response, <= 64 chars
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
			id:          "dice",
			title:       "🎲 Бросить кубик",
			description: "Отправить /dice - бросок 1-6",
			send:        "/dice",
		},
		{
			id:          "battle",
			title:       "🥊 Реакция-баттл",
			description: "Используйте: battle X Y - голосование за 60с",
			send:        "/help",
		},
		{
			id:          "quiz",
			title:       "🧩 Код-квиз",
			description: "Отправить /quiz - угадай язык",
			send:        "/quiz",
		},
		{
			id:          "stats_month",
			title:       "📊 Итоги месяца",
			description: "Отправить /stats month - номинации за месяц",
			send:        "/stats month",
		},
		{
			id:          "poll",
			title:       "📊 Опрос",
			description: "poll Вопрос | вариант1 | вариант2",
			send:        "/help",
		},
		{
			id:          "8ball",
			title:       "🎱 Шар предсказаний",
			description: "8ball <вопрос>",
			send:        "/help",
		},
		{
			id:          "roast",
			title:       "🔥 Поджарить",
			description: "Отправить /roast [@user]",
			send:        "/roast",
		},
		{
			id:          "praise",
			title:       "👏 Похвалить",
			description: "Отправить /praise [@user]",
			send:        "/praise",
		},
		{
			id:          "guess",
			title:       "🔢 Угадай число",
			description: "Отправить /guess - число 1-100",
			send:        "/guess",
		},
		{
			id:          "hangman",
			title:       "🪢 Виселица",
			description: "Отправить /hangman - IT-слова",
			send:        "/hangman",
		},
		{
			id:          "duel",
			title:       "⚔️ Дуэль",
			description: "duel @user - кубик-дуэль",
			send:        "/help",
		},
		{
			id:          "trivia",
			title:       "🎓 IT-викторина",
			description: "Отправить /trivia - вопрос с вариантами",
			send:        "/trivia",
		},
		{
			id:          "help",
			title:       "❓ Помощь",
			description: "Отправить /help - список команд",
			send:        "/help",
		},
	}
}

// BuildResults dispatches a parsed query into the inline result list.
// All results are read-only (stats variants, help, or catalog filter).
func (s *InlineService) BuildResults(ctx context.Context, query telego.InlineQuery) []telego.InlineQueryResult {
	q := strings.TrimSpace(query.Query)
	if q == "" {
		return toResults(catalog())
	}

	parts := strings.Fields(q)
	cmd := strings.ToLower(parts[0])
	args := parts[1:]
	actor := query.From

	if s.gameRouter != nil {
		if results, ok := s.gameRouter.Route(cmd, args, actor); ok {
			return results
		}
	}

	switch cmd {
	case "stats":
		return toResults(statsCommands(args))
	case "help":
		return toResults(helpCommands())
	default:
		return toResults(filterByPrefix(catalog(), q))
	}
}

// statsCommands handles only read-only /stats variants. Pure.
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
		send := "/stats " + strings.Join(args, " ")
		return []inlineCommand{{
			id:          "stats_user_" + sha1Hex(send),
			title:       "👤 " + send,
			description: "Статистика конкретного пользователя",
			send:        send,
		}}
	}
}

func helpCommands() []inlineCommand {
	return []inlineCommand{{id: "help", title: "❓ /help", description: "Список команд бота", send: "/help"}}
}

// filterByPrefix returns catalog entries whose send-text matches the
// query as a case-insensitive substring. Lets the autocomplete carousel
// shrink as the user types ("@bidlobot st" -> stats* only).
func filterByPrefix(items []inlineCommand, query string) []inlineCommand {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return items
	}
	var out []inlineCommand
	for _, item := range items {
		hay := strings.ToLower(item.send + " " + item.title + " " + item.description)
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
		article := &telego.InlineQueryResultArticle{
			Type:        telego.ResultTypeArticle,
			ID:          c.id,
			Title:       c.title,
			Description: c.description,
			InputMessageContent: &telego.InputTextMessageContent{
				MessageText: c.send,
				ParseMode:   telego.ModeHTML,
			},
		}
		results = append(results, article)
	}
	return results
}

func sha1Hex(s string) string {
	h := sha1.Sum([]byte(s))
	return fmt.Sprintf("%x", h[:6])
}

// inlineQueryHandler returns the th.InlineQueryHandler bound to the
// service. Kept narrow so routes.go does not need to know about
// InlineService internals.
func (s *InlineService) Handler() th.InlineQueryHandler {
	return func(ctx *th.Context, query telego.InlineQuery) error {
		results := s.BuildResults(ctx.Context(), query)
		err := ctx.Bot().AnswerInlineQuery(context.Background(), &telego.AnswerInlineQueryParams{
			InlineQueryID: query.ID,
			Results:       results,
			CacheTime:     0,
			IsPersonal:    true,
		})
		if err != nil {
			s.log.Warn("AnswerInlineQuery failed", "error", err, "query", query.Query, "user_id", query.From.ID)
		}
		return nil
	}
}

// htmlEscape and formatDuration are shared rendering helpers. They are
// placed here after the destructive-inline code was removed.
func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

func formatDuration(d time.Duration) string {
	day := 24 * time.Hour
	year := 365 * day
	month := 30 * day
	switch {
	case d >= year && d%year == 0:
		return fmt.Sprintf("%dг", int(d/year))
	case d >= month && d%month == 0:
		return fmt.Sprintf("%dмес", int(d/month))
	case d >= day && d%day == 0:
		return fmt.Sprintf("%dд", int(d/day))
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dч", int(d/time.Hour))
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%dм", int(d/time.Minute))
	default:
		return d.String()
	}
}
