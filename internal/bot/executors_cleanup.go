package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/domain/cleanup"
	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/domain/pending"
)

// previewListLimit caps how many candidate names get rendered in the
// preview body so a 200-candidate cleanup does not spam a single
// message past Telegram's 4096-char limit. The full count is always
// shown numerically.
const previewListLimit = 15

// CleanupExecutor implements the two-step cleanup callback flow:
//
//	v1:preview:<id>  -> render the candidate list (cleanup.PreviewInactive)
//	v1:apply:<id>    -> kick the candidates (cleanup.ExecuteCleanup, async)
type CleanupExecutor struct {
	svc     *cleanup.Service
	pending pending.Store
	bot     telegramEditor
	log     *slog.Logger

	// appCtx, when set, scopes the lifetime of background kick workers
	// to the App's lifetime. On App.Stop() the context cancels and the
	// worker stops between kicks instead of orphaning into a closed bot.
	// Falls back to context.Background() if SetAppContext is never
	// called (acceptable for tests; required wiring for production).
	appCtx context.Context

	// workers WaitGroup, shared with App.inFlight, so that App.Stop()
	// blocks on background kick workers as well as on per-update
	// handlers. Nil for tests that drive the worker synchronously.
	workers *sync.WaitGroup
}

// telegramEditor is the narrow telego surface CleanupExecutor needs to
// edit progress messages from a background goroutine. Declared here so
// tests can swap a recording stub for the real *telego.Bot.
type telegramEditor interface {
	EditMessageText(ctx context.Context, params *telego.EditMessageTextParams) (*telego.Message, error)
}

func NewCleanupExecutor(svc *cleanup.Service, pendingStore pending.Store, bot telegramEditor, log *slog.Logger) *CleanupExecutor {
	return &CleanupExecutor{
		svc:     svc,
		pending: pendingStore,
		bot:     bot,
		log:     log,
	}
}

func (e *CleanupExecutor) RegisterAll(d *CallbackDispatcher) {
	d.Register(pending.KindCleanup, cbPreview, e.ExecutePreview)
	d.Register(pending.KindCleanup, cbApply, e.ExecuteKick)
}

// SetAppContext binds the background kick worker's lifetime to the
// app-level context. Call this from main.go right after the signal-aware
// context is created; without it the worker uses context.Background()
// and will continue past App.Stop().
func (e *CleanupExecutor) SetAppContext(ctx context.Context) {
	e.appCtx = ctx
}

// AttachWaitGroup registers the App's in-flight WaitGroup so that
// Stop() waits for any kick worker that started before shutdown to
// finish its current kick (or hit the appCtx cancel between kicks).
func (e *CleanupExecutor) AttachWaitGroup(wg *sync.WaitGroup) {
	e.workers = wg
}

// ExecutePreview is the response to the "Show candidates" tap. It
// computes the candidate list freshly and edits the announcement
// message into a list view with an [Apply] [Cancel] keyboard.
func (e *CleanupExecutor) ExecutePreview(ctx context.Context, _ telego.CallbackQuery, action *pending.Action) callbackResponse {
	preview, err := e.svc.PreviewInactive(ctx, action.AbsChatID, action.Threshold, time.Now().UTC())
	if err != nil {
		return callbackResponse{
			AnswerText: "Ошибка: " + err.Error(),
			ShowAlert:  true,
		}
	}
	if len(preview.Candidates) == 0 {
		_ = e.pending.Delete(ctx, action.ID)
		return callbackResponse{
			AnswerText: "Кандидатов нет.",
			EditedText: renderEmptyPreview(preview),
		}
	}
	return callbackResponse{
		AnswerText:  fmt.Sprintf("Найдено: %d", len(preview.Candidates)),
		EditedText:  renderPreviewBody(preview),
		ReplyMarkup: cleanupConfirmKeyboard(action.ID),
	}
}

// ExecuteKick is the response to the "Kick all" tap. It re-computes the
// candidate list (so that a user who became active during the preview
// is automatically spared) and starts an async worker to do the kicks.
// Progress is reported by editing the same message; the keyboard is
// stripped so the admin cannot tap again mid-flight.
func (e *CleanupExecutor) ExecuteKick(ctx context.Context, query telego.CallbackQuery, action *pending.Action) callbackResponse {
	preview, err := e.svc.PreviewInactive(ctx, action.AbsChatID, action.Threshold, time.Now().UTC())
	if err != nil {
		return callbackResponse{
			AnswerText: "Ошибка пересчёта: " + err.Error(),
			ShowAlert:  true,
		}
	}
	if len(preview.Candidates) == 0 {
		_ = e.pending.Delete(ctx, action.ID)
		return callbackResponse{
			AnswerText: "Уже никого нет.",
			EditedText: renderEmptyPreview(preview),
		}
	}

	signed := signedChatID(query)
	chatID := signed
	messageID := query.Message.GetMessageID()
	candidates := preview.Candidates

	if e.workers != nil {
		e.workers.Add(1)
	}
	go func() {
		if e.workers != nil {
			defer e.workers.Done()
		}
		e.runWorker(action.ID, chatID, messageID, signed, candidates)
	}()

	eta := time.Duration(len(candidates)) * 2 * time.Second
	return callbackResponse{
		AnswerText:  fmt.Sprintf("Старт: %d кандидатов", len(candidates)),
		EditedText:  renderStartingBody(len(candidates), eta),
		ReplyMarkup: emptyKeyboard(),
	}
}

func (e *CleanupExecutor) runWorker(actionID string, chatID int64, messageID int, signed int64, candidates []membership.Member) {
	ctx := e.appCtx
	if ctx == nil {
		ctx = context.Background()
	}
	total := len(candidates)
	const progressEvery = 5

	report, err := e.svc.ExecuteCleanup(ctx, signed, candidates, func(done, _ int, last cleanup.ExecutionEntry) {
		if done == total {
			return // final edit happens after the loop with the full report
		}
		if done%progressEvery != 0 {
			return
		}
		body := renderProgressBody(done, total, last)
		if _, err := e.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
			ChatID:    telego.ChatID{ID: chatID},
			MessageID: messageID,
			Text:      body,
			ParseMode: telego.ModeHTML,
		}); err != nil {
			e.log.Warn("cleanup progress edit failed", "error", err, "chat_id", chatID)
		}
	})
	if err != nil {
		e.log.Warn("cleanup execute returned error", "error", err)
	}
	if _, editErr := e.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
		ChatID:    telego.ChatID{ID: chatID},
		MessageID: messageID,
		Text:      renderFinalReport(report),
		ParseMode: telego.ModeHTML,
	}); editErr != nil {
		e.log.Warn("cleanup final edit failed", "error", editErr)
	}
	_ = e.pending.Delete(ctx, actionID)
}

// ----- rendering helpers -------------------------------------------------

func renderPreviewBody(p *cleanup.Preview) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🧹 <b>Кандидаты на чистку</b> (%d из %d участников)\n\n",
		len(p.Candidates), p.KnownMembers)
	fmt.Fprintf(&b, "Период неактивности: <b>%s</b>\n", formatDuration(p.Threshold))
	if !p.InstalledAt.IsZero() {
		fmt.Fprintf(&b, "Бот наблюдает чат с %s (%s).\n",
			p.InstalledAt.Format("2 January 2006"),
			formatDuration(p.ObservationWindow))
	} else {
		b.WriteString("⚠ Бот не помнит дату своего добавления - список может быть неполным.\n")
	}
	b.WriteString("\n<b>Топ кандидатов</b> (отсортированы по самой давней активности):\n")

	limit := previewListLimit
	if len(p.Candidates) < limit {
		limit = len(p.Candidates)
	}
	for i := 0; i < limit; i++ {
		c := p.Candidates[i]
		display := "@" + c.Username
		if c.Username == "" {
			display = htmlEscape(c.FirstName)
			if display == "" {
				display = fmt.Sprintf("user %d", c.UserID)
			}
		}
		last := "никогда"
		if !c.LastSeenAt.IsZero() {
			last = c.LastSeenAt.Format("2 Jan 2006")
		}
		fmt.Fprintf(&b, "  %d. %s - последний раз: %s\n", i+1, display, last)
	}
	if len(p.Candidates) > limit {
		fmt.Fprintf(&b, "  ...и ещё %d.\n", len(p.Candidates)-limit)
	}

	b.WriteString("\n<b>Учитываются:</b> и сообщения, и реакции. Кто хотя бы раз поставил реакцию за период - в списке нет.\n")
	if n := len(p.NoEvidence); n > 0 {
		fmt.Fprintf(&b, "\nЕщё <b>%d</b> участников без единой зафиксированной активности - "+
			"это пробел в данных (вступили до начала наблюдения / только читают), "+
			"а не доказанные молчуны. В кик не войдут.\n", n)
	}
	b.WriteString("\n<i>Подтвердить кик может только инициатор.</i>")
	return b.String()
}

func renderEmptyPreview(p *cleanup.Preview) string {
	var b strings.Builder
	b.WriteString("<b>Кандидатов на чистку нет.</b>\n\n")
	if n := len(p.NoEvidence); n > 0 {
		// Do not claim "everyone is active" when the only thing we found
		// is a data gap - that was the empty-state lie.
		fmt.Fprintf(&b, "Доказанных молчунов нет, но у <b>%d</b> участников "+
			"бот не видел ни одного сообщения или реакции. Это отсутствие "+
			"данных, а не активность - проверьте вручную или загрузите "+
			"свежий экспорт через /import.\n", n)
	} else {
		fmt.Fprintf(&b, "Все %d известных боту участников активны за %s.\n",
			p.KnownMembers, formatDuration(p.Threshold))
	}
	if !p.InstalledAt.IsZero() {
		fmt.Fprintf(&b, "Бот наблюдает чат с %s.", p.InstalledAt.Format("2 January 2006"))
	}
	return b.String()
}

func renderStartingBody(total int, eta time.Duration) string {
	return fmt.Sprintf(
		"🧹 <b>Чистка запущена</b>\n\n"+
			"Кандидатов: %d\n"+
			"Ориентировочное время: %s\n\n"+
			"Перед каждым киком бот проверит актуальный статус (админ/уже вышел/бот) и пропустит лишних.\n"+
			"Прогресс будет обновляться в этом сообщении.",
		total, formatDuration(eta))
}

func renderProgressBody(done, total int, last cleanup.ExecutionEntry) string {
	bar := progressBar(done, total)
	icon, label := outcomeIconLabel(last.Outcome)
	return fmt.Sprintf(
		"🧹 <b>Чистка идёт...</b>\n\n"+
			"<code>%s</code> %d / %d\n\n"+
			"Последний: %s %s - %s",
		bar, done, total, icon, htmlEscape(last.Display), label)
}

func renderFinalReport(r *cleanup.Report) string {
	if r == nil {
		return "❌ <b>Чистка не выполнена</b> - отчёт пуст."
	}
	dur := r.FinishedAt.Sub(r.StartedAt).Round(time.Second)
	var b strings.Builder
	fmt.Fprintf(&b, "🧹 <b>Чистка завершена</b>\n\n")
	fmt.Fprintf(&b, "Кикнуто:      <b>%d</b>\n", r.Kicked)
	if r.Skipped > 0 {
		fmt.Fprintf(&b, "Пропущено:    %d\n", r.Skipped)
	}
	if r.Failed > 0 {
		fmt.Fprintf(&b, "Ошибок:       %d\n", r.Failed)
	}
	fmt.Fprintf(&b, "Всего:        %d\n", r.Total)
	fmt.Fprintf(&b, "Длительность: %s\n", formatDuration(dur))

	if r.Failed > 0 {
		b.WriteString("\n<b>Ошибки</b> (первые 5):\n")
		shown := 0
		for _, e := range r.Entries {
			if e.Outcome != cleanup.OutcomeFailed {
				continue
			}
			fmt.Fprintf(&b, "  - %s - %s\n", htmlEscape(e.Display), htmlEscape(e.APIError))
			shown++
			if shown >= 5 {
				break
			}
		}
	}
	return b.String()
}

func progressBar(done, total int) string {
	const width = 20
	if total <= 0 {
		return strings.Repeat("·", width)
	}
	filled := done * width / total
	if filled > width {
		filled = width
	}
	return strings.Repeat("█", filled) + strings.Repeat("·", width-filled)
}

func outcomeIconLabel(o cleanup.Outcome) (icon, label string) {
	switch o {
	case cleanup.OutcomeKicked:
		return "✅", "кикнут"
	case cleanup.OutcomeSkippedAdmin:
		return "👑", "стал админом, пропуск"
	case cleanup.OutcomeSkippedBot:
		return "🤖", "это бот, пропуск"
	case cleanup.OutcomeSkippedAlready:
		return "🚪", "уже не в чате"
	case cleanup.OutcomeFailed:
		return "❌", "ошибка"
	default:
		return "-", string(o)
	}
}

// ---- keyboards ----------------------------------------------------------

func cleanupConfirmKeyboard(id string) *telego.InlineKeyboardMarkup {
	return &telego.InlineKeyboardMarkup{
		InlineKeyboard: [][]telego.InlineKeyboardButton{{
			{Text: "✅ Кикнуть всех", CallbackData: MakeCallback(cbApply, id)},
			{Text: "❌ Отмена", CallbackData: MakeCallback(cbCancel, id)},
		}},
	}
}

func emptyKeyboard() *telego.InlineKeyboardMarkup {
	return &telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{}}
}
