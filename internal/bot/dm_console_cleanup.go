package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/cleanup"
	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/domain/pending"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
)

// The legacy in-DM "Stop button" worker registry (cleanupRuns) was
// removed with the immediate-kick flow: /cleanup now seeds a campaign
// driven by the daily scheduler, and the only stop is `/cleanup stop`
// (gracekick.Cancel). gracekick serializes per-chat work internally.

// handleCleanup: /cleanup <period>. Builds a preview in the DM, then a
// confirm. The actual kick loop runs after confirmation, entirely in
// the private chat, with a working Stop button.
func (d *DMConsole) handleCleanup(ctx context.Context, caller, abs int64, args []string) error {
	if len(args) == 0 {
		d.send(ctx, caller, msgDMCleanupUsage, nil)
		return nil
	}
	if d.gracekik == nil {
		d.send(ctx, caller, msgDMCleanupUnavailable, nil)
		return nil
	}

	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "stop", "cancel", "стоп", "отмена":
		n, cerr := d.gracekik.Cancel(ctx, abs)
		if cerr != nil {
			d.send(ctx, caller, msgDMError, nil)
			return nil
		}
		if n == 0 {
			d.send(ctx, caller, msgDMCleanupNoCampaign, nil)
			return nil
		}
		d.send(ctx, caller, fmt.Sprintf(msgDMCleanupStopped, n), nil)
		return nil
	}

	threshold, err := parseCleanupPeriod(args[0])
	if err != nil {
		d.send(ctx, caller, msgDMCleanupBadPeriod, nil)
		return nil
	}

	// One campaign per chat: a confirmed /cleanup starts the daily
	// public lifecycle; do not silently stack a second over the first.
	if n, cerr := d.gracekik.CampaignSize(ctx, abs); cerr == nil && n > 0 {
		d.send(ctx, caller, fmt.Sprintf(msgDMCleanupCampaignActive, n), nil)
		return nil
	}

	prev, err := d.cleanup.PreviewInactive(ctx, abs, threshold, time.Now().UTC())
	if err != nil {
		if err == cleanup.ErrThresholdTooSmall || err == cleanup.ErrThresholdTooLarge {
			d.send(ctx, caller, msgDMCleanupBadPeriod, nil)
			return nil
		}
		d.send(ctx, caller, msgDMError, nil)
		return nil
	}

	if prev.KnownMembers == 0 {
		d.send(ctx, caller, msgDMCleanupNoData, nil)
		return nil
	}

	// Resolve real Name/@handle (and live status) for the rows we will
	// show. A Telegram Desktop export has no usernames and no name for
	// join-only members, so without this the admin only ever sees
	// "id 1250985701" and the human confirm is theatre.
	const cleanupDisplayCap = 15
	stale := d.cleanup.ResolveIdentities(ctx, abs, capMembers(prev.Candidates, cleanupDisplayCap), cleanupDisplayCap)
	noEv := d.cleanup.ResolveIdentities(ctx, abs, capMembers(prev.NoEvidence, cleanupDisplayCap), cleanupDisplayCap)

	if len(prev.Candidates) == 0 {
		// No proven-inactive members. Either everyone the bot observed is
		// active, or the only "candidates" are a data gap (import-only /
		// react-only) that must never be auto-kicked - show those for
		// manual review with NO confirm keyboard instead of lying
		// "everyone is active".
		if len(prev.NoEvidence) == 0 {
			d.send(ctx, caller, fmt.Sprintf(msgDMCleanupNoneActive, prev.KnownMembers), nil)
			return nil
		}
		d.send(ctx, caller, renderCleanupPreview(prev, nil, noEv, false), nil)
		return nil
	}

	id, err := storage.NewID()
	if err != nil {
		d.send(ctx, caller, msgDMError, nil)
		return nil
	}
	now := time.Now().UTC()
	if cerr := d.pending.Create(ctx, pending.Action{
		ID:          id,
		Kind:        pending.KindCleanup,
		AbsChatID:   abs,
		ActorUserID: caller,
		Threshold:   threshold,
		CreatedAt:   now,
		ExpiresAt:   now.Add(5 * time.Minute),
	}); cerr != nil {
		d.send(ctx, caller, msgDMError, nil)
		return nil
	}

	d.send(ctx, caller, renderCleanupPreview(prev, stale, noEv, true), dmConfirmKeyboard(id))
	return nil
}

func capMembers(in []membership.Member, n int) []membership.Member {
	if len(in) > n {
		return in[:n]
	}
	return in
}

// renderCleanupPreview composes the DM preview. stale/noEv are the
// already-resolved (named, status-checked) display slices; withConfirm
// controls the footer - a confirm keyboard is attached only when there is
// a proven-inactive list that is safe to kick.
func renderCleanupPreview(p *cleanup.Preview, stale, noEv []cleanup.ResolvedMember, withConfirm bool) string {
	var b strings.Builder

	if withConfirm || len(stale) > 0 {
		fmt.Fprintf(&b, msgDMCleanupHeader, formatDuration(p.Threshold), p.KnownMembers)
	} else {
		fmt.Fprintf(&b, msgDMCleanupOnlyNoEv, len(p.NoEvidence))
	}

	// Window honesty: never let "no recorded activity" pass for
	// "inactive for the period" when the data does not cover the period.
	if p.InstalledAt.IsZero() {
		b.WriteString("\n\n" + msgDMCleanupNoInstallWarn)
	} else {
		win := p.ObservationWindow.Round(24 * time.Hour)
		fmt.Fprintf(&b, "\n"+msgDMCleanupWindow,
			p.InstalledAt.Format("2 Jan 2006"), formatDuration(win))
		if p.ThresholdExceedsWindow {
			fmt.Fprintf(&b, "\n"+msgDMCleanupWindowWarn,
				formatDuration(p.Threshold), formatDuration(win))
		}
	}

	if len(stale) > 0 {
		fmt.Fprintf(&b, msgDMCleanupStaleHeader, len(p.Candidates))
		for _, rm := range stale {
			b.WriteString("\n- " + cleanupLine(rm, true))
		}
		if extra := len(p.Candidates) - len(stale); extra > 0 {
			fmt.Fprintf(&b, "\n... и ещё %d", extra)
		}
	}

	if len(noEv) > 0 {
		fmt.Fprintf(&b, msgDMCleanupNoEvHeader, len(p.NoEvidence))
		for _, rm := range noEv {
			b.WriteString("\n- " + cleanupLine(rm, false))
		}
		if extra := len(p.NoEvidence) - len(noEv); extra > 0 {
			fmt.Fprintf(&b, "\n... и ещё %d", extra)
		}
	}

	b.WriteString(msgDMCleanupExportNote)

	if withConfirm {
		fmt.Fprintf(&b, msgDMCleanupConfirmFooter, len(p.Candidates))
	} else {
		b.WriteString(msgDMCleanupReviewOnly)
	}
	return b.String()
}

// cleanupLine renders one candidate row. The name comes from
// UserDisplayFull, which is already HTML-safe and must NOT be re-escaped.
// An unresolved member is shown honestly as a bare id, never hidden.
func cleanupLine(rm cleanup.ResolvedMember, stale bool) string {
	name := shared.UserDisplayFull(rm.Username, rm.FirstName)
	if name == "" {
		name = fmt.Sprintf("id %d - имя недоступно", rm.UserID)
	}
	switch {
	case rm.Protected:
		return name + " - админ/бот, пропустим"
	case rm.Resolved && !rm.Present:
		return name + " - уже не в чате"
	}
	if stale {
		return name + " - был(а): " + lastActivity(rm.Member)
	}
	return name + " - активности не видел"
}

func lastActivity(m membership.Member) string {
	t := m.LastMessageAt
	if m.LastReactionAt.After(t) {
		t = m.LastReactionAt
	}
	if t.IsZero() {
		t = m.LastSeenAt
	}
	if t.IsZero() {
		return "нет данных"
	}
	return t.Format("2006-01-02")
}

// HandleCallback is the DM callback entry point, namespace "dm:".
// Routes pick / apply / cancel / abort. Distinct from the public
// dispatcher: a DM is inherently private and single-actor, so there is
// no chat-pin / forward-attack surface to defend here - but we still
// verify the presser is the pending's actor.
func (d *DMConsole) HandleCallback(thctx *th.Context, q telego.CallbackQuery) error {
	ctx := thctx.Context()
	data := strings.TrimPrefix(q.Data, dmCBNamespace)
	verb, arg, found := strings.Cut(data, ":")
	if !found {
		d.answer(ctx, q, "Кнопка устарела.", false)
		return nil
	}
	caller := q.From.ID

	switch verb {
	case "pick":
		absID, perr := strconv.ParseInt(arg, 10, 64)
		if perr != nil {
			d.answer(ctx, q, "Некорректный выбор.", true)
			return nil
		}
		isAdmin, aerr := d.admin.IsAdmin(absID, caller)
		if aerr != nil || !isAdmin {
			d.answer(ctx, q, "Вы не админ в этом чате.", true)
			return nil
		}
		if err := d.sessions.Set(ctx, caller, absID, time.Now().UTC()); err != nil {
			d.answer(ctx, q, "Не удалось сохранить выбор.", true)
			return nil
		}
		title := d.chatTitle(ctx, absID)
		d.answer(ctx, q, "Выбран чат: "+title, false)
		d.editText(ctx, q, fmt.Sprintf(msgDMReady, shared.EscapeHTML(title))+dmHelpBody)
		return nil

	case "cancel":
		_ = d.pending.Delete(ctx, arg)
		d.answer(ctx, q, "Отменено.", false)
		d.editText(ctx, q, msgDMCancelled)
		return nil

	case "abort_imp":
		// Cancels a running download/ingest goroutine if any, and drops
		// + cleans a parked (awaiting-confirm) job. Idempotent.
		if d.imports != nil {
			d.cancelParked(arg)
		}
		d.answer(ctx, q, "Останавливаю...", false)
		d.editText(ctx, q, msgImportCancelled)
		return nil

	case "imp_ok":
		if d.imports == nil {
			d.answer(ctx, q, "Импорт недоступен.", true)
			return nil
		}
		return d.finishParked(ctx, q, arg)

	case "imp_no":
		if d.imports != nil {
			d.cancelParked(arg)
		}
		d.answer(ctx, q, "Отменено.", false)
		d.editText(ctx, q, msgImportCancelled)
		return nil

	case "apply":
		return d.applyPending(ctx, q, caller, arg)
	}
	d.answer(ctx, q, "Неизвестное действие.", false)
	return nil
}

func (d *DMConsole) applyPending(ctx context.Context, q telego.CallbackQuery, caller int64, id string) error {
	act, err := d.pending.Get(ctx, id)
	if err != nil {
		d.answer(ctx, q, "Действие истекло или уже выполнено.", true)
		return nil
	}
	if act.ActorUserID != caller {
		d.answer(ctx, q, "Подтвердить может только инициатор.", true)
		return nil
	}
	// Re-check admin at confirm time: a demotion between issuing and
	// confirming must not authorize a destructive action.
	if ok, aerr := d.admin.IsAdmin(act.AbsChatID, caller); aerr != nil || !ok {
		_ = d.pending.Delete(ctx, id)
		d.answer(ctx, q, "Вы больше не админ в этом чате.", true)
		return nil
	}
	signed := dmSignedChat(act.AbsChatID)

	switch act.Kind {
	case pending.KindBan:
		_ = d.pending.Delete(ctx, id)
		if err := d.mod.Ban(ctx, signed, act.TargetUserID); err != nil {
			d.answer(ctx, q, "Не удалось забанить. Проверьте право бота ограничивать участников.", true)
			// Strip the now-dead confirm keyboard: the pending is
			// already gone, re-tapping would only show "истекло".
			d.editText(ctx, q, fmt.Sprintf(msgDMBanFailed, shared.EscapeHTML(act.TargetDisplay)))
			return nil
		}
		d.answer(ctx, q, "Готово.", false)
		out := fmt.Sprintf(msgDMBanned, shared.EscapeHTML(act.TargetDisplay))
		if act.Reason != "" {
			out += "\n" + fmt.Sprintf(msgDMReasonLine, shared.EscapeHTML(act.Reason))
		}
		d.editText(ctx, q, out)
		return nil

	case pending.KindCleanup:
		_ = d.pending.Delete(ctx, id)
		return d.startCleanupCampaign(ctx, q, caller, id, act)
	}
	d.answer(ctx, q, "Неизвестный тип действия.", true)
	return nil
}

// startCleanupCampaign is the /cleanup confirm action. It does NOT kick
// now: it seeds the per-chat campaign with the freshly recomputed
// proven-stale list (names resolved live so the public tag is readable),
// after which the daily scheduler drives the tag -> grace -> kick
// lifecycle. NoEvidence is never seeded. Re-running /cleanup needs an
// explicit /cleanup stop first.
func (d *DMConsole) startCleanupCampaign(ctx context.Context, q telego.CallbackQuery, caller int64, id string, act *pending.Action) error {
	_, _ = caller, id
	if d.gracekik == nil {
		d.answer(ctx, q, "Недоступно.", true)
		d.editText(ctx, q, msgDMCleanupUnavailable)
		return nil
	}
	abs := act.AbsChatID
	now := time.Now().UTC()

	// A second admin may have started a campaign between preview and
	// this confirm - do not stack.
	if n, cerr := d.gracekik.CampaignSize(ctx, abs); cerr == nil && n > 0 {
		d.answer(ctx, q, "Уже идёт.", true)
		d.editText(ctx, q, fmt.Sprintf(msgDMCleanupCampaignActive, n))
		return nil
	}

	// Recompute proven-stale fresh: anyone active in the confirm window
	// is naturally excluded. NoEvidence is never seeded.
	prev, err := d.cleanup.PreviewInactive(ctx, abs, act.Threshold, now)
	if err != nil || prev == nil || len(prev.Candidates) == 0 {
		d.answer(ctx, q, "Кандидатов не осталось.", false)
		d.editText(ctx, q, msgDMCleanupNothingLeft)
		return nil
	}

	// Resolve names now so the public tag is readable (the export has
	// none); skip admins/bots and anyone already gone. Unresolved-but-
	// proven-stale are still seeded - the daily promote re-checks live
	// status and tags by id-link, which still pings.
	resolved := d.cleanup.ResolveIdentities(ctx, abs, prev.Candidates, len(prev.Candidates))
	seed := make([]membership.Member, 0, len(resolved))
	for _, rm := range resolved {
		if rm.Protected || (rm.Resolved && !rm.Present) {
			continue
		}
		seed = append(seed, membership.Member{
			AbsChatID: abs, UserID: rm.UserID,
			Username: rm.Username, FirstName: rm.FirstName,
		})
	}
	if len(seed) == 0 {
		d.answer(ctx, q, "Кандидатов не осталось.", false)
		d.editText(ctx, q, msgDMCleanupNothingLeft)
		return nil
	}

	n, serr := d.gracekik.Seed(ctx, abs, seed, now)
	if serr != nil {
		d.log.Warn("cleanup: seed campaign failed", "chat", abs, "error", serr)
		d.answer(ctx, q, "Ошибка запуска.", true)
		d.editText(ctx, q, msgDMError)
		return nil
	}
	d.answer(ctx, q, "Запущено.", false)
	d.editText(ctx, q, fmt.Sprintf(msgDMCleanupStarted, n))
	return nil
}

func emptyMarkup() *telego.InlineKeyboardMarkup {
	return &telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{}}
}

func (d *DMConsole) chatTitle(ctx context.Context, absID int64) string {
	if c, err := d.members.GetChat(ctx, absID); err == nil && c != nil && c.Title != "" {
		return c.Title
	}
	return fmt.Sprintf("chat %d", absID)
}

func (d *DMConsole) answer(ctx context.Context, q telego.CallbackQuery, text string, alert bool) {
	_ = d.bot.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{
		CallbackQueryID: q.ID, Text: text, ShowAlert: alert,
	})
}

func (d *DMConsole) editText(ctx context.Context, q telego.CallbackQuery, body string) {
	if q.Message == nil {
		return
	}
	d.editTextKB(ctx, q.Message.GetChat().ID, q.Message.GetMessageID(), body, emptyMarkup())
}

func (d *DMConsole) editTextKB(ctx context.Context, chatID int64, msgID int, body string, kb *telego.InlineKeyboardMarkup) {
	_, err := d.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
		ChatID:      telego.ChatID{ID: chatID},
		MessageID:   msgID,
		Text:        body,
		ParseMode:   telego.ModeHTML,
		ReplyMarkup: kb,
	})
	if err != nil {
		d.log.Warn("dm edit failed", "error", err)
	}
}

// parseCleanupPeriod accepts 7d, 30d, 6mo, 1y and bare Go durations. It
// delegates to cleanup.ParsePeriod so the DM `/cleanup` syntax and the
// daily-cleanup config can never accept different inputs.
func parseCleanupPeriod(s string) (time.Duration, error) {
	return cleanup.ParsePeriod(s)
}

var _ = membership.StatusMember
