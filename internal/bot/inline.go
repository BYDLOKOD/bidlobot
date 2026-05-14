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
	"github.com/veschin/bidlobot/internal/storage"
)

// pendingTTL bounds how long an unconfirmed inline action can sit in
// the store. Five minutes is enough for an admin to read the preview
// and decide; long enough to survive a brief lookup or attention shift,
// short enough that stale data does not pile up.
const pendingTTL = 5 * time.Minute

// InlineService backs HandleInlineQuery. The read-only queries (stats /
// warns view / help) are fully pure; destructive queries (warn / mute /
// unmute / ban / unban / cleanup) write a pending Action so that the
// callback can later validate and execute.
type InlineService struct {
	pending pending.Store
	log     *slog.Logger
}

func NewInlineService(pendingStore pending.Store, log *slog.Logger) *InlineService {
	return &InlineService{pending: pendingStore, log: log}
}

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
		{
			id:          "warn_help",
			title:       "⚠️ Предупредить",
			description: "Используйте: warn @user причина",
			send:        "/help",
		},
		{
			id:          "mute_help",
			title:       "🔇 Замьютить",
			description: "Используйте: mute @user 1h",
			send:        "/help",
		},
		{
			id:          "ban_help",
			title:       "🚫 Забанить",
			description: "Используйте: ban @user причина",
			send:        "/help",
		},
		{
			id:          "cleanup_help",
			title:       "🧹 Чистка инактивных",
			description: "Используйте: cleanup 6mo",
			send:        "/help",
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
	now := time.Now().UTC()
	actor := query.From

	switch cmd {
	case "stats":
		return toResults(statsCommands(args))
	case "warns":
		return toResults(warnsCommands(args))
	case "help":
		return toResults(helpCommands())
	case "warn":
		return s.warnPreview(ctx, actor, args, now)
	case "mute":
		return s.mutePreview(ctx, actor, args, now)
	case "unmute":
		return s.unmutePreview(ctx, actor, args, now)
	case "ban":
		return s.banPreview(ctx, actor, args, now)
	case "unban":
		return s.unbanPreview(ctx, actor, args, now)
	case "cleanup":
		return s.cleanupPreview(ctx, actor, args, now)
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

// hintResult builds a single-result reply that explains expected usage.
// Used when the query parses as a destructive command but with missing
// or malformed arguments. No pending action is created.
func hintResult(id, title, description string) []telego.InlineQueryResult {
	return toResults([]inlineCommand{{id: id, title: title, description: description, send: "/help"}})
}

// errorResult is used when something internal goes wrong building the
// preview (e.g. pending Create failed). The message is informative and
// the keyboard is empty so the user can't trigger a half-broken flow.
func errorResult(id, title, description string) []telego.InlineQueryResult {
	return toResults([]inlineCommand{{id: id, title: title, description: description, send: "Извините, временная ошибка. Повторите команду."}})
}

func (s *InlineService) warnPreview(ctx context.Context, actor telego.User, args []string, now time.Time) []telego.InlineQueryResult {
	if len(args) == 0 || !strings.HasPrefix(args[0], "@") {
		return hintResult("warn_hint", "⚠️ warn @user причина", "Укажите цель и опционально причину")
	}
	target := strings.TrimPrefix(args[0], "@")
	reason := strings.Join(args[1:], " ")
	id, err := storage.NewID()
	if err != nil {
		return errorResult("warn_err", "Ошибка", err.Error())
	}
	action := pending.Action{
		ID: id, Kind: pending.KindWarn,
		ActorUserID: actor.ID, TargetDisplay: "@" + target,
		Reason: reason, CreatedAt: now, ExpiresAt: now.Add(pendingTTL),
	}
	if err := s.pending.Create(ctx, action); err != nil {
		s.log.Warn("pending.Create warn", "error", err)
		return errorResult("warn_err", "Ошибка", err.Error())
	}
	preview := fmt.Sprintf("⚠️ Выдать предупреждение <b>@%s</b>?", htmlEscape(target))
	if reason != "" {
		preview += fmt.Sprintf("\n\n<b>Причина:</b> %s", htmlEscape(reason))
	}
	preview += "\n\n<i>Подтвердить может только инициатор.</i>"
	return toResults([]inlineCommand{{
		id:          id,
		title:       "⚠️ Предупредить @" + target,
		description: shortReason(reason, "без причины"),
		preview:     preview,
		keyboard:    confirmKeyboard(id),
	}})
}

func (s *InlineService) mutePreview(ctx context.Context, actor telego.User, args []string, now time.Time) []telego.InlineQueryResult {
	if len(args) == 0 || !strings.HasPrefix(args[0], "@") {
		return hintResult("mute_hint", "🔇 mute @user 1h", "Укажите цель и длительность (например 30m, 1h, 7d)")
	}
	target := strings.TrimPrefix(args[0], "@")
	durationStr := "1h"
	if len(args) >= 2 {
		durationStr = args[1]
	}
	duration, err := parseInlineDuration(durationStr)
	if err != nil {
		return hintResult("mute_dur_err", "🔇 Неверная длительность", "Примеры: 30m, 1h, 7d. Минимум 1m, максимум 366d.")
	}
	id, err := storage.NewID()
	if err != nil {
		return errorResult("mute_err", "Ошибка", err.Error())
	}
	action := pending.Action{
		ID: id, Kind: pending.KindMute,
		ActorUserID: actor.ID, TargetDisplay: "@" + target,
		Duration: duration, CreatedAt: now, ExpiresAt: now.Add(pendingTTL),
	}
	if err := s.pending.Create(ctx, action); err != nil {
		s.log.Warn("pending.Create mute", "error", err)
		return errorResult("mute_err", "Ошибка", err.Error())
	}
	preview := fmt.Sprintf("🔇 Заглушить <b>@%s</b> на %s?\n\n<i>Подтвердить может только инициатор.</i>",
		htmlEscape(target), formatDuration(duration))
	return toResults([]inlineCommand{{
		id:          id,
		title:       fmt.Sprintf("🔇 Заглушить @%s на %s", target, formatDuration(duration)),
		description: "Подтверждение требуется",
		preview:     preview,
		keyboard:    confirmKeyboard(id),
	}})
}

func (s *InlineService) unmutePreview(ctx context.Context, actor telego.User, args []string, now time.Time) []telego.InlineQueryResult {
	if len(args) == 0 || !strings.HasPrefix(args[0], "@") {
		return hintResult("unmute_hint", "🔊 unmute @user", "Укажите цель")
	}
	target := strings.TrimPrefix(args[0], "@")
	id, err := storage.NewID()
	if err != nil {
		return errorResult("unmute_err", "Ошибка", err.Error())
	}
	action := pending.Action{
		ID: id, Kind: pending.KindUnmute,
		ActorUserID: actor.ID, TargetDisplay: "@" + target,
		CreatedAt: now, ExpiresAt: now.Add(pendingTTL),
	}
	if err := s.pending.Create(ctx, action); err != nil {
		return errorResult("unmute_err", "Ошибка", err.Error())
	}
	preview := fmt.Sprintf("🔊 Снять mute с <b>@%s</b>?\n\n<i>Подтвердить может только инициатор.</i>", htmlEscape(target))
	return toResults([]inlineCommand{{
		id:          id,
		title:       "🔊 Снять mute с @" + target,
		description: "Подтверждение требуется",
		preview:     preview,
		keyboard:    confirmKeyboard(id),
	}})
}

func (s *InlineService) banPreview(ctx context.Context, actor telego.User, args []string, now time.Time) []telego.InlineQueryResult {
	if len(args) == 0 || !strings.HasPrefix(args[0], "@") {
		return hintResult("ban_hint", "🚫 ban @user причина", "Укажите цель и опционально причину")
	}
	target := strings.TrimPrefix(args[0], "@")
	reason := strings.Join(args[1:], " ")
	id, err := storage.NewID()
	if err != nil {
		return errorResult("ban_err", "Ошибка", err.Error())
	}
	action := pending.Action{
		ID: id, Kind: pending.KindBan,
		ActorUserID: actor.ID, TargetDisplay: "@" + target,
		Reason: reason, CreatedAt: now, ExpiresAt: now.Add(pendingTTL),
	}
	if err := s.pending.Create(ctx, action); err != nil {
		return errorResult("ban_err", "Ошибка", err.Error())
	}
	preview := fmt.Sprintf("🚫 Забанить <b>@%s</b>?", htmlEscape(target))
	if reason != "" {
		preview += fmt.Sprintf("\n\n<b>Причина:</b> %s", htmlEscape(reason))
	}
	preview += "\n\n<i>Подтвердить может только инициатор.</i>"
	return toResults([]inlineCommand{{
		id:          id,
		title:       "🚫 Забанить @" + target,
		description: shortReason(reason, "без причины"),
		preview:     preview,
		keyboard:    confirmKeyboard(id),
	}})
}

func (s *InlineService) unbanPreview(ctx context.Context, actor telego.User, args []string, now time.Time) []telego.InlineQueryResult {
	if len(args) == 0 || !strings.HasPrefix(args[0], "@") {
		return hintResult("unban_hint", "✅ unban @user", "Укажите цель")
	}
	target := strings.TrimPrefix(args[0], "@")
	id, err := storage.NewID()
	if err != nil {
		return errorResult("unban_err", "Ошибка", err.Error())
	}
	action := pending.Action{
		ID: id, Kind: pending.KindUnban,
		ActorUserID: actor.ID, TargetDisplay: "@" + target,
		CreatedAt: now, ExpiresAt: now.Add(pendingTTL),
	}
	if err := s.pending.Create(ctx, action); err != nil {
		return errorResult("unban_err", "Ошибка", err.Error())
	}
	preview := fmt.Sprintf("✅ Разбанить <b>@%s</b>?\n\n<i>Подтвердить может только инициатор.</i>", htmlEscape(target))
	return toResults([]inlineCommand{{
		id:          id,
		title:       "✅ Разбанить @" + target,
		description: "Подтверждение требуется",
		preview:     preview,
		keyboard:    confirmKeyboard(id),
	}})
}

func (s *InlineService) cleanupPreview(ctx context.Context, actor telego.User, args []string, now time.Time) []telego.InlineQueryResult {
	if len(args) == 0 {
		return hintResult("cleanup_hint", "🧹 cleanup <период>", "Например: cleanup 6mo, cleanup 30d, cleanup 1y")
	}
	threshold, err := parseInlineDuration(args[0])
	if err != nil {
		return hintResult("cleanup_err", "🧹 Неверный период", "Примеры: 7d, 30d, 6mo, 1y. Минимум 1d, максимум 5y.")
	}
	id, err := storage.NewID()
	if err != nil {
		return errorResult("cleanup_err", "Ошибка", err.Error())
	}
	action := pending.Action{
		ID: id, Kind: pending.KindCleanup,
		ActorUserID: actor.ID,
		Threshold:   threshold,
		CreatedAt:   now, ExpiresAt: now.Add(pendingTTL),
	}
	if err := s.pending.Create(ctx, action); err != nil {
		return errorResult("cleanup_err", "Ошибка", err.Error())
	}
	preview := fmt.Sprintf(
		"🧹 <b>Чистка инактивных за %s</b>\n\n"+
			"Бот посчитает участников, которые не писали и не ставили реакций "+
			"за указанный период, и покажет список перед киком.\n\n"+
			"<i>Это только превью. Реального удаления не произойдёт без второго подтверждения.</i>",
		formatDuration(threshold))
	return toResults([]inlineCommand{{
		id:          id,
		title:       "🧹 Чистка за " + formatDuration(threshold),
		description: "Открыть превью кандидатов",
		preview:     preview,
		keyboard:    cleanupPreviewKeyboard(id),
	}})
}

// confirmKeyboard is the standard [Apply] [Cancel] inline keyboard for
// moderation previews.
func confirmKeyboard(id string) *telego.InlineKeyboardMarkup {
	return &telego.InlineKeyboardMarkup{
		InlineKeyboard: [][]telego.InlineKeyboardButton{{
			{Text: "✅ Подтвердить", CallbackData: MakeCallback(cbApply, id)},
			{Text: "❌ Отмена", CallbackData: MakeCallback(cbCancel, id)},
		}},
	}
}

// cleanupPreviewKeyboard mirrors confirmKeyboard but uses the preview
// verb so the dispatcher can route to the cleanup-preview executor
// (which then renders the candidate list and a confirm keyboard).
func cleanupPreviewKeyboard(id string) *telego.InlineKeyboardMarkup {
	return &telego.InlineKeyboardMarkup{
		InlineKeyboard: [][]telego.InlineKeyboardButton{{
			{Text: "📋 Показать кандидатов", CallbackData: MakeCallback(cbPreview, id)},
			{Text: "❌ Отмена", CallbackData: MakeCallback(cbCancel, id)},
		}},
	}
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

func shortReason(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	if len(s) > 80 {
		return s[:77] + "..."
	}
	return s
}

// parseInlineDuration extends the moderation parser with calendar-style
// units (mo, y) for cleanup. Months use 30 days, years use 365 - close
// enough for an inactivity window.
func parseInlineDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	for _, suffix := range []struct {
		s    string
		mult time.Duration
	}{
		{"y", 365 * 24 * time.Hour},
		{"mo", 30 * 24 * time.Hour},
		{"w", 7 * 24 * time.Hour},
		{"d", 24 * time.Hour},
	} {
		if rest, ok := strings.CutSuffix(s, suffix.s); ok {
			n := 0
			for _, c := range rest {
				if c < '0' || c > '9' {
					return 0, fmt.Errorf("not a number: %q", rest)
				}
				n = n*10 + int(c-'0')
			}
			if n == 0 {
				return 0, fmt.Errorf("zero %s", suffix.s)
			}
			return time.Duration(n) * suffix.mult, nil
		}
	}
	return 0, fmt.Errorf("unrecognized duration: %q", s)
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
