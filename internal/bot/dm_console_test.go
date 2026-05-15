package bot

import (
	"context"
	"path/filepath"
	"strings"
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

// recSender records everything the console would send/edit/answer so a
// test can assert what the admin sees - and prove nothing leaks to the
// public chat (every send must target the admin's own user id).
type recSender struct {
	sends   []*telego.SendMessageParams
	edits   []*telego.EditMessageTextParams
	answers []*telego.AnswerCallbackQueryParams
}

func (r *recSender) SendMessage(_ context.Context, p *telego.SendMessageParams) (*telego.Message, error) {
	r.sends = append(r.sends, p)
	return &telego.Message{MessageID: 1}, nil
}
func (r *recSender) EditMessageText(_ context.Context, p *telego.EditMessageTextParams) (*telego.Message, error) {
	r.edits = append(r.edits, p)
	return &telego.Message{MessageID: 1}, nil
}
func (r *recSender) AnswerCallbackQuery(_ context.Context, p *telego.AnswerCallbackQueryParams) error {
	r.answers = append(r.answers, p)
	return nil
}
func (r *recSender) lastSendText() string {
	if len(r.sends) == 0 {
		return ""
	}
	return r.sends[len(r.sends)-1].Text
}

type dmEnv struct {
	t        *testing.T
	con      *DMConsole
	snd      *recSender
	api      *testutil.MockAPI
	members  *storage.MembershipRepo
	pending  *storage.PendingRepo
	sessions *storage.DMSessionRepo
	absChat  int64
}

const (
	dmAdminID  = int64(100)
	dmAbsChat  = int64(1001234567890)
	dmTargetID = int64(555)
)

func newDMEnv(t *testing.T) *dmEnv {
	t.Helper()
	store, err := storage.NewBoltStore(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	api := testutil.NewMockAPI()
	api.AdminIDs[dmAbsChat] = []int64{dmAdminID}
	log := testLogger()

	memberRepo := storage.NewMembershipRepo(store.DB())
	warnRepo := storage.NewWarnRepo(store.DB())
	pendingRepo := storage.NewPendingRepo(store.DB())
	sessRepo := storage.NewDMSessionRepo(store.DB())
	statsRepo := storage.NewStatsRepo(store.DB())

	adminCache := shared.NewAdminCache(api, 999, log)
	modSvc := moderation.NewService(warnRepo, api, adminCache, log)
	cleanupSvc := cleanup.NewService(memberRepo, api, log)
	cleanupSvc.SetKickInterval(time.Millisecond)
	statsBuf := stats.NewBuffer(statsRepo, log)
	statsSvc := stats.NewService(statsRepo, statsBuf, dmStubDisplay{}, log)

	// Register the chat as bot-administered with restrict rights.
	if err := memberRepo.UpsertChat(context.Background(), membership.Chat{
		AbsChatID:   dmAbsChat,
		Title:       "Тестовая",
		Type:        "supergroup",
		BotStatus:   membership.StatusAdministrator,
		CanRestrict: true,
	}); err != nil {
		t.Fatal(err)
	}

	snd := &recSender{}
	con := NewDMConsole(snd, sessRepo, memberRepo, adminCache, modSvc, cleanupSvc, statsSvc, nil, pendingRepo, log)

	return &dmEnv{
		t: t, con: con, snd: snd, api: api,
		members: memberRepo, pending: pendingRepo, sessions: sessRepo,
		absChat: dmAbsChat,
	}
}

type dmStubDisplay struct{}

func (dmStubDisplay) UserDisplay(_ context.Context, _, _ int64) string { return "" }

// dmMsg drives a DM message through the console as the admin.
func (e *dmEnv) dmMsg(text string) {
	e.t.Helper()
	thctx := th.Context{}
	_ = e.con.HandleMessage(&thctx, telego.Message{
		Chat: telego.Chat{ID: dmAdminID, Type: telego.ChatTypePrivate},
		From: &telego.User{ID: dmAdminID},
		Text: text,
	})
}

func (e *dmEnv) seedMember(id int64, username string, lastMsg time.Time) {
	e.t.Helper()
	uname := username
	_, err := e.members.UpsertMember(context.Background(), membership.MemberPatch{
		UserID: id, AbsChatID: e.absChat,
		Username: &uname, FirstName: &uname,
		Status: membership.StatusMember, KnownVia: membership.SourceImport,
		LastMessageAt: lastMsg, SetMessageCount: ptrI64(1), Now: lastMsg,
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

func ptrI64(v int64) *int64 { return &v }

func TestDMStartAutoSelectsSingleChat(t *testing.T) {
	e := newDMEnv(t)
	e.dmMsg("/start")
	got := e.snd.lastSendText()
	if !strings.Contains(got, "Тестовая") || !strings.Contains(got, "/ban") {
		t.Fatalf("start should select the chat and show the command help, got: %q", got)
	}
	s, err := e.sessions.Get(context.Background(), dmAdminID)
	if err != nil || s.AbsChatID != dmAbsChat {
		t.Fatalf("session not persisted: %v %+v", err, s)
	}
}

func TestDMNonAdminGetsNoChats(t *testing.T) {
	e := newDMEnv(t)
	thctx := th.Context{}
	_ = e.con.HandleMessage(&thctx, telego.Message{
		Chat: telego.Chat{ID: 777, Type: telego.ChatTypePrivate},
		From: &telego.User{ID: 777}, // not in AdminIDs
		Text: "/start",
	})
	got := e.snd.lastSendText()
	if !strings.Contains(got, "Не вижу чатов") {
		t.Fatalf("non-admin must be told they manage nothing, got: %q", got)
	}
	if _, err := e.sessions.Get(context.Background(), 777); err == nil {
		t.Fatal("a non-admin must NOT get a session")
	}
}

func TestDMCommandWithoutSessionNudges(t *testing.T) {
	e := newDMEnv(t)
	e.dmMsg("/ban @bob spam")
	if !strings.Contains(e.snd.lastSendText(), "/start") {
		t.Fatalf("a command before /start must nudge to /start, got: %q", e.snd.lastSendText())
	}
}

func TestDMWarnRunsPrivatelyAndStaysOffPublicChat(t *testing.T) {
	e := newDMEnv(t)
	e.seedMember(dmTargetID, "bob", time.Now())
	e.dmMsg("/start")
	e.dmMsg("/warn @bob flooding")

	last := e.snd.lastSendText()
	if !strings.Contains(last, "Предупреждение") {
		t.Fatalf("warn should confirm in DM, got: %q", last)
	}
	// Every send must target the admin's private chat, never the group.
	for _, s := range e.snd.sends {
		if s.ChatID.ID != dmAdminID {
			t.Fatalf("a message leaked outside the DM to chat %d", s.ChatID.ID)
		}
	}
}

func TestDMBanRequiresConfirmThenCallsAPI(t *testing.T) {
	e := newDMEnv(t)
	e.seedMember(dmTargetID, "bob", time.Now())
	e.dmMsg("/start")
	e.dmMsg("/ban @bob trolling")

	// A pending must exist and a confirm keyboard must be offered.
	last := e.snd.sends[len(e.snd.sends)-1]
	if last.ReplyMarkup == nil {
		t.Fatal("ban must present a confirm keyboard, not execute immediately")
	}
	if e.api.CallCount("BanChatMember") != 0 {
		t.Fatal("ban must NOT call the API before confirmation")
	}

	// Extract the apply callback data and tap it.
	kb := last.ReplyMarkup.(*telego.InlineKeyboardMarkup)
	var applyData string
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if strings.HasPrefix(b.CallbackData, dmCBNamespace+"apply:") {
				applyData = b.CallbackData
			}
		}
	}
	if applyData == "" {
		t.Fatal("no apply button found")
	}
	thctx := th.Context{}
	_ = e.con.HandleCallback(&thctx, telego.CallbackQuery{
		Data: applyData,
		From: telego.User{ID: dmAdminID},
		Message: &telego.Message{
			Chat:      telego.Chat{ID: dmAdminID, Type: telego.ChatTypePrivate},
			MessageID: 1,
		},
	})
	if e.api.CallCount("BanChatMember") != 1 {
		t.Fatalf("after confirm, ban must hit the API exactly once, got %d", e.api.CallCount("BanChatMember"))
	}
}

func TestDMBanConfirmRejectsNonInitiator(t *testing.T) {
	e := newDMEnv(t)
	e.seedMember(dmTargetID, "bob", time.Now())
	e.dmMsg("/start")
	e.dmMsg("/ban @bob trolling")
	kb := e.snd.sends[len(e.snd.sends)-1].ReplyMarkup.(*telego.InlineKeyboardMarkup)
	apply := kb.InlineKeyboard[0][0].CallbackData

	thctx := th.Context{}
	_ = e.con.HandleCallback(&thctx, telego.CallbackQuery{
		Data: apply,
		From: telego.User{ID: 999999}, // a different user
		Message: &telego.Message{
			Chat:      telego.Chat{ID: 999999, Type: telego.ChatTypePrivate},
			MessageID: 1,
		},
	})
	if e.api.CallCount("BanChatMember") != 0 {
		t.Fatal("only the initiator may confirm a ban")
	}
}

func TestDMCleanupEmptyStateDistinguishesNoData(t *testing.T) {
	e := newDMEnv(t)
	e.dmMsg("/start")
	e.dmMsg("/cleanup 6mo")
	got := e.snd.lastSendText()
	if !strings.Contains(got, "нет данных") || !strings.Contains(got, "import") {
		t.Fatalf("fresh chat must explain the import bootstrap, not say everyone is active; got: %q", got)
	}
}

func TestDMCleanupListsStaleCandidate(t *testing.T) {
	e := newDMEnv(t)
	e.seedMember(dmTargetID, "ghost", time.Now().Add(-300*24*time.Hour)) // ~10 months silent
	e.seedMember(dmAdminID, "admin", time.Now())                         // active
	e.dmMsg("/start")
	e.dmMsg("/cleanup 6mo")
	got := e.snd.lastSendText()
	if !strings.Contains(got, "Кандидатов на чистку") || !strings.Contains(got, "ghost") {
		t.Fatalf("stale member must be listed as candidate, got: %q", got)
	}
}

func TestParseCleanupPeriod(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{"7d", 7 * 24 * time.Hour, false},
		{"30d", 30 * 24 * time.Hour, false},
		{"6mo", 6 * 30 * 24 * time.Hour, false},
		{"1y", 365 * 24 * time.Hour, false},
		{"1h", time.Hour, false},
		{"0d", 0, true},
		{"xyz", 0, true},
		{"", 0, true},
	}
	for _, c := range cases {
		got, err := parseCleanupPeriod(c.in)
		if c.err {
			if err == nil {
				t.Errorf("%q: expected error", c.in)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%q: got (%v,%v) want %v", c.in, got, err, c.want)
		}
	}
}

func TestParseModDuration(t *testing.T) {
	for _, c := range []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{"30m", 30 * time.Minute, false},
		{"1h", time.Hour, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"xyz", 0, true},
		{"0m", 0, true},
	} {
		got, err := parseModDuration(c.in)
		if c.err {
			if err == nil {
				t.Errorf("%q: expected error", c.in)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%q: got (%v,%v) want %v", c.in, got, err, c.want)
		}
	}
}
