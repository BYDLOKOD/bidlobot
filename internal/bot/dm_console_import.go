package bot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/dmsession"
	"github.com/veschin/bidlobot/internal/histimport"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
)

// Telegram caps a bot file download at 20 MB regardless of the real file
// size, so a raw ~31 MB export must arrive gzipped/zipped. We reject an
// over-cap non-archive up front (before spending a getFile round-trip)
// and let WrapDecompressed bomb-guard the decompressed side at 1 GiB.
const (
	importMaxBytes        = 20 * 1024 * 1024
	importMaxDecompressed = 1 << 30
)

// fileFetcher is the narrow slice of telego.Bot the import flow needs to
// turn a Document.FileID into a downloadable URL. An interface so tests
// fake it; *telego.Bot satisfies it directly in production.
type fileFetcher interface {
	GetFile(ctx context.Context, p *telego.GetFileParams) (*telego.File, error)
	FileDownloadURL(filePath string) string
}

// parkedImport is a download+parsed export waiting for the admin's
// explicit "load" tap. The temp file holds the (size- and
// decompress-capped) JSON; it MUST be removed on every terminal path
// (imp_ok done / imp_no / abort / stale eviction). parkedAt bounds an
// abandoned confirm: an admin who uploads, sees the preview, then walks
// away would otherwise pin the chat claim and leak the temp file
// forever - the await TTL only governs the pre-upload window, not a
// parked job. A later /import on the same chat evicts a parked entry
// older than this.
type parkedImport struct {
	tmpPath   string
	absChatID int64
	chatID    int64 // the DM chat to edit (== admin user id)
	msgID     int
	caller    int64
	parkedAt  time.Time
}

// parkedTTL bounds how long an unconfirmed parked import keeps the chat
// claimed and its temp file on disk. Equal to the await TTL: a confirm
// prompt ignored longer than the time it took to produce the export is
// abandoned.
const parkedTTL = dmsession.ImportAwaitTTL

// importRuns is a per-chat single-flight guard plus a cancel func per
// run id, extended with a parked-job registry so the
// pre-commit confirm can resume the (already downloaded+parsed) import
// without re-downloading. A chat stays claimed from start() until a
// terminal transition (finishParked / abort / failed download), so a
// second admin cannot launch a concurrent import on the same chat while
// the first is mid-download OR sitting on the confirm prompt.
type importRuns struct {
	mu          sync.Mutex
	runs        map[string]context.CancelFunc
	activeChats map[int64]bool
	parked      map[string]parkedImport
}

func newImportRuns() *importRuns {
	return &importRuns{
		runs:        make(map[string]context.CancelFunc),
		activeChats: make(map[int64]bool),
		parked:      make(map[string]parkedImport),
	}
}

// NewImportRuns is the exported constructor for the import single-flight
// registry, so cmd/bidlobot can build the dependency without exporting
// the registry's internals. Tests use the unexported newImportRuns.
func NewImportRuns() *importRuns { return newImportRuns() } //nolint:revive // intentional unexported-return: opaque handle, only NewDMConsole consumes it

// start atomically registers a run id + cancel and claims the chat.
// Returns false (registering nothing) if the chat already has a live
// active or parked import. A parked job older than parkedTTL is treated
// as abandoned: its temp file is removed and its claim freed so this new
// import can proceed (self-healing - no background sweeper needed for
// the common "admin forgot to confirm" case).
func (c *importRuns) start(id string, absChatID int64, cancel context.CancelFunc) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeChats[absChatID] {
		if !c.evictStaleParkedLocked(absChatID) {
			return false
		}
	}
	c.runs[id] = cancel
	c.activeChats[absChatID] = true
	return true
}

// evictStaleParkedLocked drops every parked job for absChatID that is
// older than parkedTTL, removing its temp file and freeing the chat
// claim. Returns true if the chat is now free to reclaim. Caller holds
// c.mu. A still-fresh parked job (admin actively looking at the confirm)
// is left intact and the chat stays claimed.
func (c *importRuns) evictStaleParkedLocked(absChatID int64) bool {
	now := time.Now()
	evicted := false
	for id, p := range c.parked {
		if p.absChatID != absChatID {
			continue
		}
		if now.Sub(p.parkedAt) < parkedTTL {
			return false // a fresh parked job still owns this chat.
		}
		_ = os.Remove(p.tmpPath)
		delete(c.parked, id)
		delete(c.runs, id)
		evicted = true
	}
	if evicted {
		delete(c.activeChats, absChatID)
	}
	// If the chat was "active" with NO parked entry, it is a live
	// download/ingest goroutine - do not evict that.
	return evicted
}

// park records a downloaded+parsed job awaiting confirmation. The run id
// is removed from runs (the download goroutine is done) but the chat
// stays claimed and the cancel func remains reachable via abort through
// the parked entry's own context (the goroutine already returned, so the
// only remaining cancelation is temp-file removal on imp_no/abort).
func (c *importRuns) park(id string, p parkedImport) {
	c.mu.Lock()
	delete(c.runs, id)
	c.parked[id] = p
	c.mu.Unlock()
}

// peekParked returns a parked job WITHOUT removing it, for the
// authorization checks (initiator / not-found). The single-use claim is
// takeParked. Splitting peek from take removes the take-then-re-park
// window that could race a concurrent cancel into a leaked chat claim +
// orphaned temp file.
func (c *importRuns) peekParked(id string) (parkedImport, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.parked[id]
	return p, ok
}

// takeParked atomically removes and returns a parked job (single-use:
// double-tap of "load" must not ingest twice from the registry side).
func (c *importRuns) takeParked(id string) (parkedImport, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.parked[id]
	if ok {
		delete(c.parked, id)
	}
	return p, ok
}

// reregister attaches a fresh cancel func to an already-claimed run id
// (the ingest goroutine started from the imp_ok callback reuses the
// chat claim taken at upload time, so the Stop button keeps working
// across the confirm boundary).
func (c *importRuns) reregister(id string, cancel context.CancelFunc) {
	c.mu.Lock()
	c.runs[id] = cancel
	c.mu.Unlock()
}

// cancel cancels an in-flight download/ingest goroutine by id. Returns
// true if a cancel func was registered (i.e. a goroutine is running).
func (c *importRuns) cancel(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if fn, ok := c.runs[id]; ok {
		fn()
		delete(c.runs, id)
		return true
	}
	return false
}

// releaseChat frees the chat claim for a finished run id. Safe to call
// even if the chat was never claimed under this id.
func (c *importRuns) releaseChat(id string, absChatID int64) {
	c.mu.Lock()
	delete(c.runs, id)
	delete(c.parked, id)
	delete(c.activeChats, absChatID)
	c.mu.Unlock()
}

// handleImportStart implements /import: bind the upload to the admin's
// currently-selected managed chat and arm the await window. The actual
// ingest happens when the admin uploads the file (handleImportDocument).
func (d *DMConsole) handleImportStart(ctx context.Context, caller int64) error {
	abs, _, ok := d.requireSession(ctx, caller)
	if !ok {
		return nil // requireSession already nudged the admin.
	}
	// Minimal/test app without the import wiring: degrade explicitly
	// rather than arm a window nothing can service.
	if d.importState == nil || d.imports == nil || d.files == nil ||
		d.memberRepo == nil || d.monthRepo == nil {
		d.send(ctx, caller, msgImportUnavailable, nil)
		return nil
	}
	now := time.Now().UTC()
	if err := d.importState.Set(ctx, dmsession.ImportState{
		AdminUserID: caller,
		AbsChatID:   abs,
		StartedAt:   now,
		ExpiresAt:   now.Add(dmsession.ImportAwaitTTL),
	}); err != nil {
		d.send(ctx, caller, msgDMError, nil)
		return nil
	}
	d.send(ctx, caller, msgImportAwait, nil)
	return nil
}

// handleImportDocument runs when a document arrives in the DM. It is
// gated on a live /import window for this admin; the upload is bound to
// the chat selected at /import time and re-authorized here.
func (d *DMConsole) handleImportDocument(thctx *th.Context, msg telego.Message) error {
	ctx := thctx.Context()
	caller := msg.From.ID

	if d.importState == nil || d.imports == nil || d.files == nil ||
		d.memberRepo == nil || d.monthRepo == nil {
		// No import wiring: a stray document gets the same hint as a
		// document with no /import context (nothing armed it).
		d.send(ctx, caller, msgImportNoContext, nil)
		return nil
	}

	st, err := d.importState.Get(ctx, caller)
	if err != nil {
		if errors.Is(err, dmsession.ErrNoImportAwait) {
			d.send(ctx, caller, msgImportNoContext, nil)
			return nil
		}
		d.send(ctx, caller, msgDMError, nil)
		return nil
	}

	// Re-authorize: a demotion between /import and the upload must not
	// let a stale window write into a chat the admin no longer controls.
	if ok, aerr := d.admin.IsAdmin(st.AbsChatID, caller); aerr != nil || !ok {
		_ = d.importState.Clear(ctx, caller)
		d.send(ctx, caller, msgDMLostAdmin, nil)
		return nil
	}

	doc := msg.Document
	abs := st.AbsChatID

	// Up-front size gate: Telegram refuses to even hand a >20 MB file to
	// a bot. If it is not an archive there is nothing we can do, so fail
	// before spending a getFile round-trip. An archive over 20 MB is
	// still unfetchable, but the message already tells the admin to
	// slice by date; the precise getFile error is mapped below too.
	if doc.FileSize > importMaxBytes && !isArchiveName(doc.FileName) {
		d.send(ctx, caller, msgImportTooBig, nil)
		return nil
	}

	// The await window is single-use: consume it now so a second stray
	// document does not re-enter this path while the job runs.
	_ = d.importState.Clear(ctx, caller)

	id, err := storage.NewID()
	if err != nil {
		d.send(ctx, caller, msgDMError, nil)
		return nil
	}

	runCtx, cancel := context.WithCancel(d.appCtx())
	if !d.imports.start(id, abs, cancel) {
		cancel()
		d.send(ctx, caller, msgImportAlreadyRunning, nil)
		return nil
	}

	progressMsg, sendErr := d.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: caller},
		Text:      msgImportParsing,
		ParseMode: telego.ModeHTML,
		ReplyMarkup: &telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{{
			{Text: "✕ Остановить", CallbackData: dmCBNamespace + "abort_imp:" + id},
		}}},
	})
	if sendErr != nil || progressMsg == nil {
		// Could not even open the progress message: release the claim so
		// the admin can retry, do not leak the goroutine.
		d.imports.releaseChat(id, abs)
		cancel()
		d.log.Warn("import progress send failed", "error", sendErr, "user_id", caller)
		return nil
	}
	// In a DM the chat id IS the caller's user id; do not depend on the
	// API echoing Chat back on the sent message.
	chatID := caller
	msgID := progressMsg.MessageID
	title := d.chatTitle(ctx, abs)

	// Capture the WaitGroup once: when no WaitGroup is attached d.wg()
	// returns a fresh throwaway each call, so Add and Done MUST act on
	// the same instance or the counter goes negative.
	wg := d.wg()
	wg.Add(1)
	go func() {
		defer wg.Done()
		// On any path that does NOT successfully park, this run's chat
		// claim is released and the cancel func fired here. The parked
		// path releases the claim later (finishParked / abort).
		parked := false
		defer func() {
			cancel()
			if !parked {
				d.imports.releaseChat(id, abs)
			}
		}()

		tmpPath, ok := d.downloadAndStage(runCtx, doc, chatID, msgID, id)
		if !ok {
			return // downloadAndStage already edited the progress message.
		}

		// Pre-commit preview: parse once (membership-only quick scan,
		// nil sink) just to show counts. The temp file is reopened and
		// fully ingested only on confirm, so a stream cannot be parsed
		// twice in place.
		stats, perr := d.parseForPreview(runCtx, tmpPath)
		if perr != nil {
			_ = os.Remove(tmpPath)
			d.editProgress(chatID, msgID, mapParseError(perr), emptyMarkup())
			return
		}

		d.imports.park(id, parkedImport{
			tmpPath:   tmpPath,
			absChatID: abs,
			chatID:    chatID,
			msgID:     msgID,
			caller:    caller,
			parkedAt:  time.Now(),
		})
		parked = true
		d.editProgress(chatID, msgID, renderImportConfirm(title, stats),
			&telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{{
				{Text: "✅ Загрузить", CallbackData: dmCBNamespace + "imp_ok:" + id},
				{Text: "✕ Отмена", CallbackData: dmCBNamespace + "imp_no:" + id},
			}}})
	}()
	return nil
}

// downloadAndStage resolves the file, downloads it under the size cap,
// decompresses, and spills the JSON to a temp file under the bbolt data
// dir / os.TempDir. Returns the temp path or false (after editing the
// progress message with a specific Russian reason). The caller owns
// removing the temp file on every later path.
func (d *DMConsole) downloadAndStage(ctx context.Context, doc *telego.Document, chatID int64, msgID int, id string) (string, bool) {
	f, err := d.files.GetFile(ctx, &telego.GetFileParams{FileID: doc.FileID})
	if err != nil {
		msg := msgImportDownloadFail
		if isTooBigErr(err) {
			msg = msgImportTooBig
		}
		d.editProgress(chatID, msgID, msg, emptyMarkup())
		return "", false
	}
	if f == nil || f.FilePath == "" {
		d.editProgress(chatID, msgID, msgImportDownloadFail, emptyMarkup())
		return "", false
	}

	url := d.files.FileDownloadURL(f.FilePath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		d.editProgress(chatID, msgID, msgImportDownloadFail, emptyMarkup())
		return "", false
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			d.editProgress(chatID, msgID, msgImportCancelled, emptyMarkup())
		} else {
			d.editProgress(chatID, msgID, msgImportDownloadFail, emptyMarkup())
		}
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		d.editProgress(chatID, msgID, msgImportLinkExpired, emptyMarkup())
		return "", false
	}

	// Cap the wire read: Telegram should never serve >20 MB to a bot,
	// but a hostile/buggy endpoint must not stream us out of memory.
	body := io.LimitReader(resp.Body, importMaxBytes+1)
	rc, err := histimport.WrapDecompressed(doc.FileName, body, importMaxDecompressed)
	if err != nil {
		d.editProgress(chatID, msgID, mapParseError(err), emptyMarkup())
		return "", false
	}
	defer rc.Close()

	tmp, err := os.CreateTemp(d.importTempDir(), "bidlobot-import-stage-*.json")
	if err != nil {
		d.editProgress(chatID, msgID, msgDMError, emptyMarkup())
		return "", false
	}
	tmpPath := tmp.Name()
	// Bound the spill too: WrapDecompressed already guards the
	// decompressed side, this caps the on-disk artifact independently.
	_, copyErr := io.Copy(tmp, io.LimitReader(rc, importMaxDecompressed+1))
	closeErr := tmp.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		if ctx.Err() != nil {
			d.editProgress(chatID, msgID, msgImportCancelled, emptyMarkup())
		} else {
			d.editProgress(chatID, msgID, mapParseError(copyErr), emptyMarkup())
		}
		return "", false
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		d.editProgress(chatID, msgID, msgDMError, emptyMarkup())
		return "", false
	}
	return tmpPath, true
}

// parseForPreview streams the staged temp file once with a nil sink
// (membership-only quick scan) to produce the counts shown in the
// confirm prompt. It does NOT write anything.
func (d *DMConsole) parseForPreview(ctx context.Context, tmpPath string) (*histimport.Stats, error) {
	rf, err := os.Open(tmpPath)
	if err != nil {
		return nil, err
	}
	defer rf.Close()
	return histimport.Parse(ctx, rf, nil, false)
}

// finishParked is invoked by the imp_ok callback: reopen the staged temp
// file and run the full idempotent ingest (membership + monthly), with a
// throttled progress edit and a working Stop button. Partial commit is
// acceptable - histimport.Ingest is idempotent (message-id watermark +
// atomic ApplyImport), so a re-tap or a re-upload re-skips correctly.
func (d *DMConsole) finishParked(ctx context.Context, q telego.CallbackQuery, id string) error {
	// Peek (no removal) for the authorization checks so the
	// initiator-only rejection cannot race a concurrent cancel/abort
	// into a leaked claim + orphaned temp file.
	p, ok := d.imports.peekParked(id)
	if !ok {
		d.answer(ctx, q, "Нечего загружать - возможно, уже выполнено или отменено.", true)
		return nil
	}
	if q.From.ID != p.caller {
		// Pure read above: nothing was removed, nothing to re-park.
		d.answer(ctx, q, "Подтвердить может только инициатор.", true)
		return nil
	}
	// Initiator confirmed: now atomically claim the single-use job. If a
	// concurrent abort/cancel won the race it already cleaned up (temp
	// removed, chat released), so an empty take is a clean no-op.
	p, ok = d.imports.takeParked(id)
	if !ok {
		d.answer(ctx, q, "Нечего загружать - возможно, уже выполнено или отменено.", true)
		return nil
	}
	// Re-authorize at confirm time: a demotion between upload and
	// confirm must not let the ingest land in a chat the admin lost.
	if okAdmin, aerr := d.admin.IsAdmin(p.absChatID, p.caller); aerr != nil || !okAdmin {
		_ = os.Remove(p.tmpPath)
		d.imports.releaseChat(id, p.absChatID)
		d.answer(ctx, q, "Вы больше не админ в этом чате.", true)
		d.editProgress(p.chatID, p.msgID, msgDMLostAdmin, emptyMarkup())
		return nil
	}

	d.answer(ctx, q, "Загружаю...", false)

	runCtx, cancel := context.WithCancel(d.appCtx())
	// Reuse the chat claim: it is still held from start(); register the
	// new cancel under the same id so the Stop button keeps working
	// through the ingest phase.
	d.imports.reregister(id, cancel)

	d.editProgress(p.chatID, p.msgID, fmt.Sprintf(msgImportProgress, 0, 0),
		&telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{{
			{Text: "✕ Остановить", CallbackData: dmCBNamespace + "abort_imp:" + id},
		}}})

	wg := d.wg()
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer d.imports.releaseChat(id, p.absChatID)
		defer cancel()
		defer os.Remove(p.tmpPath)

		rf, oerr := os.Open(p.tmpPath)
		if oerr != nil {
			d.editProgress(p.chatID, p.msgID, msgDMError, emptyMarkup())
			return
		}
		defer rf.Close()

		var lastEdit time.Time
		res, ierr := histimport.Ingest(runCtx, rf, p.absChatID, d.memberRepo, d.monthRepo,
			func(done, total int) {
				if time.Since(lastEdit) < 3*time.Second && done < total {
					return
				}
				lastEdit = time.Now()
				d.editProgress(p.chatID, p.msgID,
					fmt.Sprintf(msgImportProgress, done, total),
					&telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{{
						{Text: "✕ Остановить", CallbackData: dmCBNamespace + "abort_imp:" + id},
					}}})
			}, false)
		if ierr != nil {
			if runCtx.Err() != nil {
				// Aborted mid-ingest. Whatever committed before the
				// cancel is idempotent; a re-import re-skips it.
				d.editProgress(p.chatID, p.msgID, msgImportCancelled, emptyMarkup())
				return
			}
			d.editProgress(p.chatID, p.msgID, mapParseError(ierr), emptyMarkup())
			return
		}
		d.editProgress(p.chatID, p.msgID, histimport.FormatDMReport(res), emptyMarkup())
	}()
	return nil
}

// cancelParked handles imp_no / abort_imp: cancel a running goroutine if
// any, drop the parked job, and remove the staged temp file. Idempotent.
func (d *DMConsole) cancelParked(id string) {
	// Cancel a running download/ingest goroutine (if mid-flight).
	d.imports.cancel(id)
	// Drop and clean a parked (awaiting-confirm) job.
	if p, ok := d.imports.takeParked(id); ok {
		_ = os.Remove(p.tmpPath)
		d.imports.releaseChat(id, p.absChatID)
	}
}

// editProgress edits the import progress/confirm message. Uses a
// detached context so a shutdown still flushes the final state best
// effort (mirrors the cleanup flow).
func (d *DMConsole) editProgress(chatID int64, msgID int, body string, kb *telego.InlineKeyboardMarkup) {
	d.editTextKB(context.Background(), chatID, msgID, body, kb)
}

// importTempDir keeps the staged JSON next to the bbolt data dir when
// known, else os.TempDir. Either way every path defer-removes it.
func (d *DMConsole) importTempDir() string {
	if d.importStageDir != "" {
		return d.importStageDir
	}
	return os.TempDir()
}

func renderImportConfirm(title string, st *histimport.Stats) string {
	period := "неизвестен"
	if !st.Earliest.IsZero() {
		period = fmt.Sprintf("%s - %s",
			st.Earliest.Format("2006-01-02"), st.Latest.Format("2006-01-02"))
	}
	top := topThree(st)
	return fmt.Sprintf(msgImportConfirm,
		shared.EscapeHTML(title),
		shared.FormatNumber(st.TotalMessages),
		len(st.Users),
		period,
		top,
	)
}

// topThree renders the three most active users for the confirm prompt so
// the admin can sanity-check the export is the right chat before writing.
func topThree(st *histimport.Stats) string {
	type ua struct {
		name  string
		count int64
	}
	all := make([]ua, 0, len(st.Users))
	for _, a := range st.Users {
		n := a.FirstName
		if n == "" {
			n = fmt.Sprintf("user %d", a.UserID)
		}
		all = append(all, ua{name: n, count: a.Count})
	}
	// Simple selection of top-3 (the user set is bounded; no need for a
	// full sort just to show three lines).
	for i := 0; i < len(all) && i < 3; i++ {
		maxJ := i
		for j := i + 1; j < len(all); j++ {
			if all[j].count > all[maxJ].count {
				maxJ = j
			}
		}
		all[i], all[maxJ] = all[maxJ], all[i]
	}
	var b strings.Builder
	b.WriteString("Активнее всех:")
	n := len(all)
	if n > 3 {
		n = 3
	}
	if n == 0 {
		return "Активнее всех: -"
	}
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "\n%d. %s - %s", i+1,
			shared.EscapeHTML(all[i].name), shared.FormatNumber(all[i].count))
	}
	return b.String()
}

// mapParseError turns a decompress/parse failure into a specific Russian
// remediation message instead of leaking an opaque Go error to the admin.
func mapParseError(err error) string {
	switch {
	case errors.Is(err, histimport.ErrNoJSONInZip):
		return msgImportBadArchive
	case errors.Is(err, histimport.ErrDecompressLimit):
		return msgImportTooBig
	default:
		return msgImportBadArchive
	}
}

// isArchiveName reports whether the filename hints at a container we can
// decompress. WrapDecompressed sniffs magic bytes regardless, but the
// up-front size gate uses the name only (the bytes are not fetched yet).
func isArchiveName(name string) bool {
	n := strings.ToLower(name)
	return strings.HasSuffix(n, ".gz") || strings.HasSuffix(n, ".zip") ||
		strings.HasSuffix(n, ".tgz")
}

// isTooBigErr matches Telegram's "file is too big" getFile rejection so
// it maps to the actionable "zip/slice it" message rather than a generic
// download failure.
func isTooBigErr(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "too big") || strings.Contains(s, "file is too big")
}
