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

	"github.com/veschin/bidlobot/internal/domain/pending"
)

// InlineService backs HandleInlineQuery. The read-only queries (stats /
// warns view / help) are fully pure; destructive queries (warn / mute /
// unmute / ban / unban / cleanup) write a pending Action so that the
// callback can later validate and execute.
type InlineService struct {
	pending pending.Store
	log     *slog.Logger

	// gameRouter is consulted before the moderation/cleanup branches so
	// that mini-game commands (dice/battle/quiz) can register without
	// the inline package importing the games packages. Returns
	// (results, true) when the command was handled; (nil, false) when
	// the inline service should continue with the default switch.
	gameRouter InlineGameRouter
}

// InlineGameRouter is the contract a mini-game module implements to plug
// itself into inline-mode dispatch. The router receives the lowercased
// first token (cmd) and the trailing tokens (args). Implementations must
// be cheap and side-effect-free: inline queries fire on every keystroke.
type InlineGameRouter interface {
	Route(cmd string, args []string, actor telego.User) (results []telego.InlineQueryResult, handled bool)
}

func NewInlineService(pendingStore pending.Store, log *slog.Logger) *InlineService {
	return &InlineService{pending: pendingStore, log: log}
}

// SetGameRouter wires a router that handles dice/battle/quiz inline
// queries. Idempotent; passing nil disables routing.
func (s *InlineService) SetGameRouter(r InlineGameRouter) { s.gameRouter = r }

// inlineCommand describes one offer the bot suggests when the user types
// "@bidlobot ..." in any chat. For read-only entries the result fires
// a regular slash-command message in the destination chat. For
// destructive entries the result is a preview text with two callback
// buttons; tapping Apply confirms via the dispatcher.
type inlineCommand struct {
	id          string                       // stable identifier inside an inline-query response, <= 64 chars
	title       string                       // shown in the inline carousel
	description string                       // shown one line below the title
	send        string                       // the slash-command text Telegram will insert into the chat (read-only entries only)
	preview     string                       // the message body for destructive previews (with reply_markup)
	keyboard    *telego.InlineKeyboardMarkup // attached to the destructive preview
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
		// Moderation is intentionally ABSENT from the inline catalog.
		// Inline results post publicly into the chat, so moderation can
		// never run here - advertising warn/mute/ban/cleanup in the
		// browsable list shows functions that cannot be done where the
		// user is looking. Moderation lives only in the DM console.
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
			id:          "help",
			title:       "❓ Помощь",
			description: "Отправить /help - список команд",
			send:        "/help",
		},
	}
}

// BuildResults dispatches a parsed query into the inline result list.
// Destructive commands write a pending action via s.pending; read-only
// branches stay pure.
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
	case "warn", "warns", "mute", "unmute", "ban", "unban", "cleanup":
		// Moderation is DM-only. Inline results are posted publicly
		// into the chat, so they can never be a private control
		// surface - this is the architectural reason the old inline
		// previews were removed. Point the admin to the DM console.
		return dmRedirectInline()
	default:
		return toResults(filterByPrefix(catalog(), q))
	}
}

// dmRedirectInline is the single result shown when an admin tries any
// moderation verb via inline. Selecting it posts a short, harmless note
// (no target, no action) and tells them to use the private console.
func dmRedirectInline() []telego.InlineQueryResult {
	return toResults([]inlineCommand{{
		id:          "mod_dm_only",
		title:       "Модерация - только в личке",
		description: "Откройте чат со мной и отправьте /start",
		send:        "Модерация бота доступна только в личке (так участники её не видят). Откройте личный чат со мной и отправьте /start.",
	}})
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
		text := c.send
		if c.preview != "" {
			text = c.preview
		}
		article := &telego.InlineQueryResultArticle{
			Type:        telego.ResultTypeArticle,
			ID:          c.id,
			Title:       c.title,
			Description: c.description,
			InputMessageContent: &telego.InputTextMessageContent{
				MessageText: text,
				ParseMode:   telego.ModeHTML,
			},
		}
		if c.keyboard != nil {
			article.ReplyMarkup = c.keyboard
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


// htmlEscape and formatDuration are shared rendering helpers used by the
// callback executors (kept here after the inline destructive previews
// were removed; they are not inline-specific).
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
