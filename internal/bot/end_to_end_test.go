package bot

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/domain/cleanup"
	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/domain/moderation"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
	"github.com/veschin/bidlobot/internal/testutil"
)

// e2eEnv assembles the full production wiring against bbolt + a mock
// Telegram API. Anything you can do via inline + callback in real life
// can be exercised here without touching the network.
type e2eEnv struct {
	t            *testing.T
	store        *storage.BoltStore
	api          *testutil.MockAPI
	pending      *storage.PendingRepo
	members      *storage.MembershipRepo
	warns        *storage.WarnRepo
	adminCache   *shared.AdminCache
	mod          *moderation.Service
	cleanup      *cleanup.Service
	dispatcher   *CallbackDispatcher
	inline       *InlineService
	modExec      *ModerationExecutor
	cleanupExec  *CleanupExecutor
	chatIDSigned int64
	chatIDAbs    int64
	editor       *recordingEditor
}

func newE2E(t *testing.T) *e2eEnv {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "e2e.db")
	store, err := storage.NewBoltStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	const chatAbs = int64(1001234567890)
	const chatSigned = -chatAbs

	api := testutil.NewMockAPI()
	api.AdminIDs[chatAbs] = []int64{100} // user 100 is admin

	log := testLogger()
	pendingRepo := storage.NewPendingRepo(store.DB())
	memberRepo := storage.NewMembershipRepo(store.DB())
	warnRepo := storage.NewWarnRepo(store.DB())
	adminCache := shared.NewAdminCache(api, 999, log)

	modSvc := moderation.NewService(warnRepo, api, adminCache, log)
	cleanupSvc := cleanup.NewService(memberRepo, api, log)
	cleanupSvc.SetKickInterval(time.Millisecond)

	editor := newRecordingEditor(100)
	dispatcher := NewCallbackDispatcher(pendingRepo, adminCache, nil, log)
	inline := NewInlineService(pendingRepo, log)

	modExec := NewModerationExecutor(modSvc, memberRepo, adminCache, log)
	modExec.RegisterAll(dispatcher)

	cleanupExec := NewCleanupExecutor(cleanupSvc, pendingRepo, editor, log)
	cleanupExec.RegisterAll(dispatcher)

	return &e2eEnv{
		t:            t,
		store:        store,
		api:          api,
		pending:      pendingRepo,
		members:      memberRepo,
		warns:        warnRepo,
		adminCache:   adminCache,
		mod:          modSvc,
		cleanup:      cleanupSvc,
		dispatcher:   dispatcher,
		inline:       inline,
		modExec:      modExec,
		cleanupExec:  cleanupExec,
		chatIDSigned: chatSigned,
		chatIDAbs:    chatAbs,
		editor:       editor,
	}
}

func (e *e2eEnv) inlineResults(actorID int64, query string) []telego.InlineQueryResult {
	return e.inline.BuildResults(context.Background(), telego.InlineQuery{
		Query: query,
		From:  telego.User{ID: actorID},
	})
}

// confirmTap extracts the callback_data of the [✅] button from the
// first inline result and runs the dispatcher against it as if the
// user had tapped it.
func (e *e2eEnv) confirmTap(actorID int64, results []telego.InlineQueryResult) callbackResponse {
	e.t.Helper()
	if len(results) == 0 {
		e.t.Fatal("no inline results to tap")
	}
	article, ok := results[0].(*telego.InlineQueryResultArticle)
	if !ok {
		e.t.Fatalf("first result not an article: %T", results[0])
	}
	if article.ReplyMarkup == nil {
		e.t.Fatal("first result has no keyboard - destructive previews must carry one")
	}
	var data string
	for _, row := range article.ReplyMarkup.InlineKeyboard {
		for _, btn := range row {
			if strings.HasPrefix(btn.Text, "✅") {
				data = btn.CallbackData
			}
		}
	}
	if data == "" {
		e.t.Fatal("no apply button found in keyboard")
	}
	return e.dispatcher.dispatch(context.Background(), telego.CallbackQuery{
		ID:   "cb",
		Data: data,
		From: telego.User{ID: actorID},
		Message: &telego.Message{
			MessageID: 42,
			Chat:      telego.Chat{ID: e.chatIDSigned, Type: telego.ChatTypeSupergroup},
		},
	})
}

func TestE2EWarnFromInlineToBboltRecord(t *testing.T) {
	env := newE2E(t)

	// Bob exists in membership so resolve@username works.
	env.members.UpsertMember(context.Background(), membership.MemberPatch{
		UserID: 200, AbsChatID: env.chatIDAbs,
		Username: ptrStr("bob"), FirstName: ptrStr("Bob"),
		Status: membership.StatusMember, Now: time.Now(),
	})

	// Admin 100 inlines `warn @bob spam`.
	results := env.inlineResults(100, "warn @bob spam")

	// Tap the apply button.
	resp := env.confirmTap(100, results)

	if !strings.Contains(resp.EditedText, "@bob") {
		t.Fatalf("response should mention target, got %q", resp.EditedText)
	}
	if !strings.Contains(resp.EditedText, "spam") {
		t.Fatalf("response should mention reason, got %q", resp.EditedText)
	}

	// Bbolt now has one active warning.
	active, err := env.warns.ListActive(context.Background(), 200, env.chatIDAbs)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("expected 1 active warning, got %d", len(active))
	}
	if active[0].Reason != "spam" || active[0].IssuerUserID != 100 {
		t.Fatalf("warning fields wrong: %+v", active[0])
	}
}

func TestE2EWarnRejectsNonAdminCaller(t *testing.T) {
	env := newE2E(t)
	env.members.UpsertMember(context.Background(), membership.MemberPatch{
		UserID: 200, AbsChatID: env.chatIDAbs,
		Username: ptrStr("bob"), Status: membership.StatusMember, Now: time.Now(),
	})

	results := env.inlineResults(500, "warn @bob spam") // 500 not admin
	resp := env.confirmTap(500, results)
	if !resp.ShowAlert {
		t.Fatalf("non-admin caller must hit alert, got %+v", resp)
	}

	// No warning created.
	active, _ := env.warns.ListActive(context.Background(), 200, env.chatIDAbs)
	if len(active) != 0 {
		t.Fatalf("non-admin must not be able to create warnings, got %d", len(active))
	}
}

func TestE2EBanFromInlineMakesAPICall(t *testing.T) {
	env := newE2E(t)
	env.members.UpsertMember(context.Background(), membership.MemberPatch{
		UserID: 200, AbsChatID: env.chatIDAbs,
		Username: ptrStr("bob"), Status: membership.StatusMember, Now: time.Now(),
	})

	results := env.inlineResults(100, "ban @bob raid")
	resp := env.confirmTap(100, results)
	if !strings.Contains(resp.EditedText, "@bob") {
		t.Fatalf("expected ban confirmation, got %q", resp.EditedText)
	}
	if env.api.CallCount("BanChatMember") != 1 {
		t.Fatalf("expected 1 BanChatMember call, got %d", env.api.CallCount("BanChatMember"))
	}
}

func TestE2EUnknownTargetIsBlocked(t *testing.T) {
	env := newE2E(t)
	// no member upserted -> @ghost cannot be resolved

	results := env.inlineResults(100, "warn @ghost spam")
	resp := env.confirmTap(100, results)
	if !resp.ShowAlert {
		t.Fatalf("unknown target must alert, got %+v", resp)
	}
}

func TestE2ESecondActorCannotConfirm(t *testing.T) {
	env := newE2E(t)
	env.members.UpsertMember(context.Background(), membership.MemberPatch{
		UserID: 200, AbsChatID: env.chatIDAbs,
		Username: ptrStr("bob"), Status: membership.StatusMember, Now: time.Now(),
	})

	// Admin 100 issues the inline preview.
	results := env.inlineResults(100, "warn @bob spam")

	// A different user (even an admin) tries to apply it - not allowed.
	api := env.api
	api.AdminIDs[env.chatIDAbs] = []int64{100, 999}
	resp := env.confirmTap(999, results)
	if !resp.ShowAlert {
		t.Fatalf("non-issuer apply must alert, got %+v", resp)
	}
}

func TestE2ECleanupShowsCandidatesAndKicks(t *testing.T) {
	env := newE2E(t)
	now := time.Now().UTC()

	// Three inactive members.
	for _, uid := range []int64{200, 300, 400} {
		env.members.UpsertMember(context.Background(), membership.MemberPatch{
			UserID: uid, AbsChatID: env.chatIDAbs,
			Status: membership.StatusMember, Now: now.Add(-200 * 24 * time.Hour),
		})
	}
	// Set their LastSeenAt to long ago directly so cleanup considers
	// them candidates (UpsertMember sets LastSeenAt to Now).
	// Actually - Now: now.Add(-200d) above already flowed into LastSeenAt
	// via the upsert's "laterOf". They're inactive against a 30d threshold.

	// Admin invokes "cleanup 30d" inline - gets the announcement.
	announce := env.inlineResults(100, "cleanup 30d")
	if len(announce) != 1 {
		t.Fatalf("expected 1 announcement, got %d", len(announce))
	}
	article := announce[0].(*telego.InlineQueryResultArticle)

	// Find the preview button (📋 Show candidates) - uses cbPreview verb.
	var previewData string
	for _, row := range article.ReplyMarkup.InlineKeyboard {
		for _, btn := range row {
			if strings.Contains(btn.CallbackData, ":preview:") {
				previewData = btn.CallbackData
			}
		}
	}
	if previewData == "" {
		t.Fatal("no preview button in cleanup announcement keyboard")
	}

	// Tap "Show candidates".
	previewResp := env.dispatcher.dispatch(context.Background(), telego.CallbackQuery{
		Data: previewData,
		From: telego.User{ID: 100},
		Message: &telego.Message{
			MessageID: 42,
			Chat:      telego.Chat{ID: env.chatIDSigned, Type: telego.ChatTypeSupergroup},
		},
	})
	if !strings.Contains(previewResp.EditedText, "Кандидаты") {
		t.Fatalf("preview should list candidates, got %q", previewResp.EditedText)
	}
	if previewResp.ReplyMarkup == nil {
		t.Fatal("preview must carry confirm keyboard")
	}

	// Find the apply button.
	var applyData string
	for _, row := range previewResp.ReplyMarkup.InlineKeyboard {
		for _, btn := range row {
			if strings.Contains(btn.CallbackData, ":apply:") {
				applyData = btn.CallbackData
			}
		}
	}
	if applyData == "" {
		t.Fatal("no apply button in cleanup confirm keyboard")
	}

	// Tap "Kick all".
	kickResp := env.dispatcher.dispatch(context.Background(), telego.CallbackQuery{
		Data: applyData,
		From: telego.User{ID: 100},
		Message: &telego.Message{
			MessageID: 42,
			Chat:      telego.Chat{ID: env.chatIDSigned, Type: telego.ChatTypeSupergroup},
		},
	})
	if !strings.Contains(kickResp.EditedText, "Чистка запущена") {
		t.Fatalf("kick response should announce launch, got %q", kickResp.EditedText)
	}

	// Wait for the worker (1ms kick interval) and assert the API got called.
	deadline := time.After(2 * time.Second)
	for {
		if env.api.CallCount("BanChatMember") >= 3 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("worker did not perform 3 kicks, BanChatMember called %d times", env.api.CallCount("BanChatMember"))
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func ptrStr(s string) *string { return &s }
