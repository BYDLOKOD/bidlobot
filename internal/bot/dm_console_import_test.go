package bot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/cleanup"
	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/domain/moderation"
	"github.com/veschin/bidlobot/internal/domain/stats"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
	"github.com/veschin/bidlobot/internal/testutil"
)

// syncRecSender is a thread-safe dmSender. The import flow edits the
// progress/confirm message from a background goroutine while the test
// polls for it, so unlike the single-threaded recSender every access is
// mutex-guarded (the bare recSender would race under -race here, which
// would be a test-harness defect, not a production one - the production
// code is correctly goroutine-bounded).
type syncRecSender struct {
	mu      sync.Mutex
	sends   []*telego.SendMessageParams
	edits   []*telego.EditMessageTextParams
	answers []*telego.AnswerCallbackQueryParams
}

func (r *syncRecSender) SendMessage(_ context.Context, p *telego.SendMessageParams) (*telego.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sends = append(r.sends, p)
	return &telego.Message{MessageID: 1}, nil
}

func (r *syncRecSender) EditMessageText(_ context.Context, p *telego.EditMessageTextParams) (*telego.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.edits = append(r.edits, p)
	return &telego.Message{MessageID: 1}, nil
}

func (r *syncRecSender) AnswerCallbackQuery(_ context.Context, p *telego.AnswerCallbackQueryParams) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answers = append(r.answers, p)
	return nil
}

func (r *syncRecSender) lastSendText() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.sends) == 0 {
		return ""
	}
	return r.sends[len(r.sends)-1].Text
}

func (r *syncRecSender) lastEditText() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.edits) == 0 {
		return ""
	}
	return r.edits[len(r.edits)-1].Text
}

// findCB scans every recorded send AND edit for an inline button whose
// callback_data starts with prefix (newest first).
func (r *syncRecSender) findCB(prefix string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	scan := func(kb *telego.InlineKeyboardMarkup) string {
		if kb == nil {
			return ""
		}
		for _, row := range kb.InlineKeyboard {
			for _, b := range row {
				if strings.HasPrefix(b.CallbackData, prefix) {
					return b.CallbackData
				}
			}
		}
		return ""
	}
	for i := len(r.sends) - 1; i >= 0; i-- {
		if kb, ok := r.sends[i].ReplyMarkup.(*telego.InlineKeyboardMarkup); ok {
			if d := scan(kb); d != "" {
				return d
			}
		}
	}
	for i := len(r.edits) - 1; i >= 0; i-- {
		if d := scan(r.edits[i].ReplyMarkup); d != "" {
			return d
		}
	}
	return ""
}

// editContains reports whether any recorded edit's text contains sub.
func (r *syncRecSender) editContains(sub string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.edits) - 1; i >= 0; i-- {
		if strings.Contains(r.edits[i].Text, sub) {
			return r.edits[i].Text, true
		}
	}
	return "", false
}

// assertNoPublicLeak proves every send and edit targeted the admin's own
// private chat - the whole point of the DM console.
func (r *syncRecSender) assertNoPublicLeak(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.sends {
		if s.ChatID.ID != dmAdminID {
			t.Fatalf("send leaked to chat %d (not the admin DM)", s.ChatID.ID)
		}
	}
	for _, ed := range r.edits {
		if ed.ChatID.ID != dmAdminID {
			t.Fatalf("edit leaked to chat %d (not the admin DM)", ed.ChatID.ID)
		}
	}
}

// fakeFetcher is a fileFetcher backed by an httptest server. It records
// the GetFile call count so a test can assert the oversize precheck
// short-circuits BEFORE a getFile round-trip is spent.
type fakeFetcher struct {
	mu        sync.Mutex
	baseURL   string
	filePath  string
	getCalls  int
	getErr    error
	emptyPath bool
}

func (f *fakeFetcher) GetFile(_ context.Context, _ *telego.GetFileParams) (*telego.File, error) {
	f.mu.Lock()
	f.getCalls++
	gerr := f.getErr
	empty := f.emptyPath
	fp := f.filePath
	f.mu.Unlock()
	if gerr != nil {
		return nil, gerr
	}
	if empty {
		return &telego.File{}, nil
	}
	return &telego.File{FilePath: fp}, nil
}

func (f *fakeFetcher) FileDownloadURL(filePath string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.baseURL + "/" + filePath
}

func (f *fakeFetcher) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.getCalls
}

// syntheticExport is a minimal Telegram-Desktop "Export chat history"
// JSON: two real users, one service join, one channel auto-post. It
// exercises the same decode path the real ~31 MB export does.
func syntheticExport() string {
	ts := func(d string) int64 {
		t, _ := time.Parse("2006-01-02", d)
		return t.Unix()
	}
	return fmt.Sprintf(`{
  "name": "Synthetic Chat",
  "type": "public_supergroup",
  "messages": [
    {"id": 9,  "type": "service", "date_unixtime": "%d", "action": "create_group", "actor": "Alice", "actor_id": "user111", "text": ""},
    {"id": 10, "type": "message", "date_unixtime": "%d", "from": "Alice", "from_id": "user111", "text": "hello world from alice"},
    {"id": 11, "type": "message", "date_unixtime": "%d", "from": "Alice", "from_id": "user111", "text": "second alice message"},
    {"id": 12, "type": "message", "date_unixtime": "%d", "from": "Bob", "from_id": "user222", "text": "bob says hi"},
    {"id": 13, "type": "message", "date_unixtime": "%d", "from": "Alice", "from_id": "user111", "text": "third one"},
    {"id": 14, "type": "message", "date_unixtime": "%d", "from": "channel", "from_id": "channel999", "text": "auto post ignored"}
  ]
}`,
		ts("2026-02-09"),
		ts("2026-02-10"), ts("2026-02-11"),
		ts("2026-02-20"),
		ts("2026-03-01"),
		ts("2026-03-05"))
}

type dmImportEnv struct {
	t       *testing.T
	con     *DMConsole
	snd     *syncRecSender
	api     *testutil.MockAPI
	members *storage.MembershipRepo
	month   *storage.MonthStatsRepo
	istate  *storage.ImportStateRepo
	fetch   *fakeFetcher
	srv     *httptest.Server
	absChat int64
}

func newDMImportEnv(t *testing.T, exportBody string) *dmImportEnv {
	t.Helper()
	store, err := storage.NewBoltStore(filepath.Join(t.TempDir(), "imp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	api := testutil.NewMockAPI()
	api.AdminIDs[dmAbsChat] = []int64{dmAdminID}
	log := testLogger()

	memberRepo := storage.NewMembershipRepo(store.DB())
	monthRepo := storage.NewMonthStatsRepo(store.DB())
	istate := storage.NewImportStateRepo(store.DB())
	warnRepo := storage.NewWarnRepo(store.DB())
	pendingRepo := storage.NewPendingRepo(store.DB())
	sessRepo := storage.NewDMSessionRepo(store.DB())
	statsRepo := storage.NewStatsRepo(store.DB())

	adminCache := shared.NewAdminCache(api, 999, log)
	modSvc := moderation.NewService(warnRepo, api, adminCache, log)
	cleanupSvc := cleanup.NewService(memberRepo, api, log)
	statsBuf := stats.NewBuffer(statsRepo, log)
	statsSvc := stats.NewService(statsRepo, statsBuf, dmStubDisplay{}, log)

	if err := memberRepo.UpsertChat(context.Background(), membership.Chat{
		AbsChatID:   dmAbsChat,
		Title:       "Тестовая",
		Type:        "supergroup",
		BotStatus:   membership.StatusAdministrator,
		CanRestrict: true,
	}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/missing") {
			http.Error(w, "gone", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(exportBody))
	}))
	t.Cleanup(srv.Close)

	fetch := &fakeFetcher{baseURL: srv.URL, filePath: "doc/export.json"}

	snd := &syncRecSender{}
	con := NewDMConsole(snd, sessRepo, memberRepo, adminCache, modSvc, cleanupSvc, statsSvc, nil, pendingRepo,
		istate, NewImportRuns(), fetch, memberRepo, monthRepo, log)
	con.importStageDir = t.TempDir()

	return &dmImportEnv{
		t: t, con: con, snd: snd, api: api,
		members: memberRepo, month: monthRepo, istate: istate,
		fetch: fetch, srv: srv, absChat: dmAbsChat,
	}
}

func (e *dmImportEnv) msg(text string) {
	e.t.Helper()
	thctx := th.Context{}
	_ = e.con.HandleMessage(&thctx, telego.Message{
		Chat: telego.Chat{ID: dmAdminID, Type: telego.ChatTypePrivate},
		From: &telego.User{ID: dmAdminID},
		Text: text,
	})
}

func (e *dmImportEnv) doc(name string, size int64) {
	e.t.Helper()
	thctx := th.Context{}
	_ = e.con.HandleMessage(&thctx, telego.Message{
		Chat:     telego.Chat{ID: dmAdminID, Type: telego.ChatTypePrivate},
		From:     &telego.User{ID: dmAdminID},
		Document: &telego.Document{FileID: "FID", FileName: name, FileSize: size},
	})
}

func (e *dmImportEnv) tap(data string) {
	e.t.Helper()
	thctx := th.Context{}
	_ = e.con.HandleCallback(&thctx, telego.CallbackQuery{
		Data: data,
		From: telego.User{ID: dmAdminID},
		Message: &telego.Message{
			Chat:      telego.Chat{ID: dmAdminID, Type: telego.ChatTypePrivate},
			MessageID: 1,
		},
	})
}

func TestImportNoSessionNudges(t *testing.T) {
	e := newDMImportEnv(t, syntheticExport())
	e.msg("/import")
	if !strings.Contains(e.snd.lastSendText(), "/start") {
		t.Fatalf("/import without a session must nudge to /start, got: %q", e.snd.lastSendText())
	}
	if _, err := e.istate.Get(context.Background(), dmAdminID); err == nil {
		t.Fatal("no import state may be armed without a session")
	}
}

func TestImportWithSessionArmsAwait(t *testing.T) {
	e := newDMImportEnv(t, syntheticExport())
	e.msg("/start")
	e.msg("/import")
	got := e.snd.lastSendText()
	if !strings.Contains(got, "Экспорт истории чата") || !strings.Contains(got, "10 минут") {
		t.Fatalf("/import must explain how to export and the 10-min window, got: %q", got)
	}
	st, err := e.istate.Get(context.Background(), dmAdminID)
	if err != nil || st.AbsChatID != dmAbsChat {
		t.Fatalf("import state not armed for the selected chat: %v %+v", err, st)
	}
}

func TestImportDocumentWithoutContextHint(t *testing.T) {
	e := newDMImportEnv(t, syntheticExport())
	e.msg("/start")
	// A document with NO prior /import.
	e.doc("export.json", 1024)
	if !strings.Contains(e.snd.lastSendText(), "Сначала отправьте /import") {
		t.Fatalf("a stray document must get the no-context hint, got: %q", e.snd.lastSendText())
	}
	if e.fetch.calls() != 0 {
		t.Fatalf("no /import context must not trigger a getFile, got %d", e.fetch.calls())
	}
}

func TestImportOversizeNonArchiveRejectedBeforeGetFile(t *testing.T) {
	e := newDMImportEnv(t, syntheticExport())
	e.msg("/start")
	e.msg("/import")
	// 21 MB .json: over the bot download cap and not an archive.
	e.doc("export.json", 21*1024*1024)
	got := e.snd.lastSendText()
	if !strings.Contains(got, "больше 20 МБ") {
		t.Fatalf("oversize non-archive must be rejected with the size message, got: %q", got)
	}
	if e.fetch.calls() != 0 {
		t.Fatalf("oversize precheck must short-circuit BEFORE getFile, got %d calls", e.fetch.calls())
	}
	// The await state must remain so the admin can resend a zipped file
	// without re-running /import.
	if _, err := e.istate.Get(context.Background(), dmAdminID); err != nil {
		t.Fatalf("await state must survive an oversize rejection, got %v", err)
	}
}

func TestImportHappyPathConfirmThenIngest(t *testing.T) {
	e := newDMImportEnv(t, syntheticExport())
	e.msg("/start")
	e.msg("/import")
	e.doc("export.json", 2048)

	// The background goroutine downloads + parses then edits the progress
	// message into a confirm. Wait for the imp_ok button to appear.
	confirm := waitCB(t, e, dmCBNamespace+"imp_ok:")
	confirmText := e.snd.lastEditText()
	if !strings.Contains(confirmText, "Проверьте перед загрузкой") {
		t.Fatalf("a pre-commit confirm must be shown, got: %q", confirmText)
	}
	if !strings.Contains(confirmText, "Alice") {
		t.Fatalf("confirm should preview the top user, got: %q", confirmText)
	}
	if !strings.Contains(confirmText, "Тестовая") {
		t.Fatalf("confirm must name the target chat, got: %q", confirmText)
	}

	// Confirm -> ingest. Wait for the final report edit.
	e.tap(confirm)
	report := waitEditContains(t, e, "Импорт истории завершён")
	if !strings.Contains(report, "Топ-10") {
		t.Fatalf("final report missing, got: %q", report)
	}

	// Membership written for both real users (cleanup substrate).
	if m, err := e.members.GetMember(context.Background(), 111, dmAbsChat); err != nil || m == nil {
		t.Fatalf("Alice membership not written: %v %+v", err, m)
	}
	if m, err := e.members.GetMember(context.Background(), 222, dmAbsChat); err != nil || m == nil {
		t.Fatalf("Bob membership not written: %v %+v", err, m)
	}
	// channel999 is not a "user" id and must be excluded.
	if m, _ := e.members.GetMember(context.Background(), 999, dmAbsChat); m != nil {
		t.Fatal("channel auto-post author must NOT become a member")
	}

	// Monthly stats written too (/stats month substrate).
	meta, _, err := e.month.GetMonth(context.Background(), dmAbsChat, "2026-02")
	if err != nil || meta == nil || meta.TotalMsgs == 0 {
		t.Fatalf("2026-02 monthly not written: %v %+v", err, meta)
	}
	st, serr := e.month.GetState(context.Background(), dmAbsChat)
	if serr != nil || st == nil || st.ImportHWM == 0 {
		t.Fatalf("import watermark not advanced: %v %+v", serr, st)
	}
	firstHWM := st.ImportHWM

	e.snd.assertNoPublicLeak(t)

	// Idempotency: re-arm, re-upload, re-confirm. The watermark dedups
	// every row, so monthly totals must not double and the watermark
	// must not move.
	febBefore := monthTotalMsgs(t, e, "2026-02")
	e.msg("/import")
	e.doc("export.json", 2048)
	confirm2 := waitCB(t, e, dmCBNamespace+"imp_ok:")
	e.tap(confirm2)
	waitEditContains(t, e, "Импорт истории завершён")

	febAfter := monthTotalMsgs(t, e, "2026-02")
	if febAfter != febBefore {
		t.Fatalf("re-import double-counted monthly: before=%d after=%d", febBefore, febAfter)
	}
	st2, _ := e.month.GetState(context.Background(), dmAbsChat)
	if st2.ImportHWM != firstHWM {
		t.Fatalf("watermark moved on a duplicate import: %d -> %d", firstHWM, st2.ImportHWM)
	}
	e.snd.assertNoPublicLeak(t)
}

func TestImportSecondConcurrentSameChatRefused(t *testing.T) {
	e := newDMImportEnv(t, syntheticExport())
	e.msg("/start")
	e.msg("/import")
	e.doc("export.json", 2048)
	// First upload parks awaiting confirm; the chat stays claimed.
	waitCB(t, e, dmCBNamespace+"imp_ok:")

	// A second /import + upload for the SAME chat while the first sits on
	// the confirm prompt must be refused.
	e.msg("/import")
	e.doc("export.json", 2048)
	if !strings.Contains(e.snd.lastSendText(), "уже идёт импорт") {
		t.Fatalf("a second concurrent import on the same chat must be refused, got: %q", e.snd.lastSendText())
	}
}

func TestImportCancelRemovesParkedJob(t *testing.T) {
	e := newDMImportEnv(t, syntheticExport())
	e.msg("/start")
	e.msg("/import")
	e.doc("export.json", 2048)
	waitCB(t, e, dmCBNamespace+"imp_ok:")
	cancelData := e.snd.findCB(dmCBNamespace + "imp_no:")
	if cancelData == "" {
		t.Fatal("a parked import must offer a cancel button")
	}
	e.tap(cancelData)
	if _, ok := e.snd.editContains("отменён"); !ok {
		t.Fatalf("cancel must report the import was cancelled, got: %q", e.snd.lastEditText())
	}
	// After cancel the chat is free: a fresh /import + upload parks again.
	e.msg("/import")
	e.doc("export.json", 2048)
	if got := waitCB(t, e, dmCBNamespace+"imp_ok:"); got == "" {
		t.Fatal("after cancel a new import on the same chat must be allowed")
	}
}

func TestImportStaleParkedJobEvictedOnNextImport(t *testing.T) {
	e := newDMImportEnv(t, syntheticExport())
	e.msg("/start")
	e.msg("/import")
	e.doc("export.json", 2048)
	waitCB(t, e, dmCBNamespace+"imp_ok:")

	// Simulate an abandoned confirm: backdate the parked job past its
	// TTL without tapping Load/Cancel. The temp file and chat claim
	// would otherwise leak forever.
	var stalePath string
	e.con.imports.mu.Lock()
	for id, p := range e.con.imports.parked {
		p.parkedAt = time.Now().Add(-parkedTTL - time.Minute)
		e.con.imports.parked[id] = p
		stalePath = p.tmpPath
	}
	e.con.imports.mu.Unlock()
	if stalePath == "" {
		t.Fatal("expected a parked job to backdate")
	}
	if _, err := os.Stat(stalePath); err != nil {
		t.Fatalf("staged temp file should exist before eviction: %v", err)
	}

	// A fresh /import on the same chat must evict the stale parked job
	// (free the claim, remove its temp file) and proceed.
	e.msg("/import")
	e.doc("export.json", 2048)
	if got := waitCB(t, e, dmCBNamespace+"imp_ok:"); got == "" {
		t.Fatal("a stale parked job must not block a new import on the same chat")
	}
	if _, err := os.Stat(stalePath); err == nil {
		t.Fatal("evicting a stale parked job must remove its temp file")
	}
}

func TestImportLinkExpiredReported(t *testing.T) {
	e := newDMImportEnv(t, syntheticExport())
	e.fetch.filePath = "doc/missing" // server returns 404 for this path
	e.msg("/start")
	e.msg("/import")
	e.doc("export.json", 2048)
	got := waitEditContains(t, e, "устарела")
	if !strings.Contains(got, "Пришлите файл заново") {
		t.Fatalf("a non-200 download must map to the link-expired message, got: %q", got)
	}
}

func TestImportUnavailableOnMinimalApp(t *testing.T) {
	// The plain dm env (newDMEnv) has no import wiring: /import must say
	// unavailable and a stray document must get the no-context hint.
	e := newDMEnv(t)
	e.dmMsg("/start")
	e.dmMsg("/import")
	if !strings.Contains(e.snd.lastSendText(), "недоступен") {
		t.Fatalf("minimal app must report /import unavailable, got: %q", e.snd.lastSendText())
	}
	thctx := th.Context{}
	_ = e.con.HandleMessage(&thctx, telego.Message{
		Chat:     telego.Chat{ID: dmAdminID, Type: telego.ChatTypePrivate},
		From:     &telego.User{ID: dmAdminID},
		Document: &telego.Document{FileID: "X", FileName: "x.json", FileSize: 10},
	})
	if !strings.Contains(e.snd.lastSendText(), "Сначала отправьте /import") {
		t.Fatalf("minimal app must ignore a stray document with the hint, got: %q", e.snd.lastSendText())
	}
}

// --- polling helpers: the download/ingest runs in a goroutine ---

func waitCB(t *testing.T, e *dmImportEnv, prefix string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if d := e.snd.findCB(prefix); d != "" {
			return d
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for callback %q; last edit: %q", prefix, e.snd.lastEditText())
	return ""
}

func waitEditContains(t *testing.T, e *dmImportEnv, sub string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if txt, ok := e.snd.editContains(sub); ok {
			return txt
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for an edit containing %q; last edit: %q", sub, e.snd.lastEditText())
	return ""
}

func monthTotalMsgs(t *testing.T, e *dmImportEnv, month string) int64 {
	t.Helper()
	meta, _, err := e.month.GetMonth(context.Background(), dmAbsChat, month)
	if err != nil {
		t.Fatalf("GetMonth(%s): %v", month, err)
	}
	if meta == nil {
		return 0
	}
	return meta.TotalMsgs
}
