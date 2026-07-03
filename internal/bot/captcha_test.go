package bot

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/captcha"
	"github.com/veschin/bidlobot/internal/testutil"
	"github.com/veschin/bidlobot/internal/text"
)

// lastEditText returns the Text of the most recent EditMessageText call, or "".
func lastEditText(api *testutil.MockAPI) string {
	for i := len(api.Calls) - 1; i >= 0; i-- {
		if api.Calls[i].Method == "EditMessageText" {
			if p, ok := api.Calls[i].Params.(*telego.EditMessageTextParams); ok {
				return p.Text
			}
		}
	}
	return ""
}

// lastAnimationCaption returns the Caption of the most recent SendAnimation call, or "".
func lastAnimationCaption(api *testutil.MockAPI) string {
	for i := len(api.Calls) - 1; i >= 0; i-- {
		if api.Calls[i].Method == "SendAnimation" {
			if p, ok := api.Calls[i].Params.(*telego.SendAnimationParams); ok {
				return p.Caption
			}
		}
	}
	return ""
}

// waitForSendAnimation polls until OnAnswer's async sendWelcome goroutine
// records a SendAnimation call, or fails after 2s.
func waitForSendAnimation(t *testing.T, api *testutil.MockAPI) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if api.CallCount("SendAnimation") > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for async SendAnimation")
}

// botFakeStore is an in-memory captcha.Store for the handler-level tests.
type botFakeStore struct {
	mu   sync.Mutex
	byID map[string]captcha.Challenge
}

func newBotFakeStore() *botFakeStore { return &botFakeStore{byID: map[string]captcha.Challenge{}} }

func (s *botFakeStore) Create(_ context.Context, c captcha.Challenge) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[c.ID] = c
	return nil
}
func (s *botFakeStore) Get(_ context.Context, id string) (*captcha.Challenge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.byID[id]; ok {
		return &c, nil
	}
	return nil, captcha.ErrNotFound
}
func (s *botFakeStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, id)
	return nil
}
func (s *botFakeStore) GetByUser(_ context.Context, absChatID, userID int64) (*captcha.Challenge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.byID {
		if c.AbsChatID == absChatID && c.UserID == userID {
			return &c, nil
		}
	}
	return nil, captcha.ErrNotFound
}
func (s *botFakeStore) ListExpired(_ context.Context, now time.Time) ([]captcha.Challenge, error) {
	return nil, nil
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newCaptchaSvc(t *testing.T) (*captcha.Service, *botFakeStore, *testutil.MockAPI) {
	t.Helper()
	api := testutil.NewMockAPI()
	store := newBotFakeStore()
	return captcha.NewService(store, api, discardLogger(), 10*time.Minute), store, api
}

func supergroupJoinUpdate(old, new telego.ChatMember) telego.ChatMemberUpdated {
	return telego.ChatMemberUpdated{
		Chat:          telego.Chat{ID: -200, Type: telego.ChatTypeSupergroup},
		Date:          time.Now().Unix(),
		OldChatMember: old,
		NewChatMember: new,
	}
}

// TestCaptchaChatMemberHandlerFiresOnNewJoin: left/kicked -> member triggers a captcha.
func TestCaptchaChatMemberHandlerFiresOnNewJoin(t *testing.T) {
	t.Parallel()
	svc, _, api := newCaptchaSvc(t)
	h := captchaChatMemberHandler(svc, discardLogger())

	user := telego.User{ID: 100, FirstName: "Alice"}
	cmu := supergroupJoinUpdate(
		&telego.ChatMemberLeft{User: user},
		&telego.ChatMemberMember{User: user},
	)
	if err := h(&th.Context{}, cmu); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got := len(api.Messages); got != 1 {
		t.Fatalf("new join must post exactly one captcha message, got %d", got)
	}
}

// TestCaptchaChatMemberHandlerIgnoresUnmute: restricted -> member is the
// service's own unmute path and MUST NOT re-captcha (else an infinite loop).
func TestCaptchaChatMemberHandlerIgnoresUnmute(t *testing.T) {
	t.Parallel()
	svc, _, api := newCaptchaSvc(t)
	h := captchaChatMemberHandler(svc, discardLogger())

	user := telego.User{ID: 100, FirstName: "Alice"}
	cmu := supergroupJoinUpdate(
		&telego.ChatMemberRestricted{User: user},
		&telego.ChatMemberMember{User: user},
	)
	if err := h(&th.Context{}, cmu); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got := len(api.Messages); got != 0 {
		t.Fatalf("restricted->member must not post a captcha, got %d messages", got)
	}
}

// TestCaptchaChatMemberHandlerSkipsBots: a bot joining is never captchad.
func TestCaptchaChatMemberHandlerSkipsBots(t *testing.T) {
	t.Parallel()
	svc, _, api := newCaptchaSvc(t)
	h := captchaChatMemberHandler(svc, discardLogger())

	bot := telego.User{ID: 777, IsBot: true}
	cmu := supergroupJoinUpdate(
		&telego.ChatMemberLeft{User: bot},
		&telego.ChatMemberMember{User: bot},
	)
	_ = h(&th.Context{}, cmu)
	if got := len(api.Messages); got != 0 {
		t.Fatalf("a bot join must not be captchad, got %d messages", got)
	}
}

// TestCaptchaChatMemberHandlerNilTolerant: a nil service (feature off) is a no-op.
func TestCaptchaChatMemberHandlerNilTolerant(t *testing.T) {
	t.Parallel()
	h := captchaChatMemberHandler(nil, discardLogger())
	user := telego.User{ID: 100}
	cmu := supergroupJoinUpdate(
		&telego.ChatMemberLeft{User: user},
		&telego.ChatMemberMember{User: user},
	)
	if err := h(&th.Context{}, cmu); err != nil {
		t.Fatalf("nil service must be a no-op, got %v", err)
	}
}

// TestCaptchaFullFlowThroughHandlers: join posts a captcha; a correct answer
// tap resolves it via the callback handler (the whole wiring in one test).
func TestCaptchaFullFlowThroughHandlers(t *testing.T) {
	t.Parallel()
	svc, store, api := newCaptchaSvc(t)
	memH := captchaChatMemberHandler(svc, discardLogger())
	cbH := captchaCallbackHandler(svc, discardLogger())

	user := telego.User{ID: 100, FirstName: "Alice"}
	_ = memH(&th.Context{}, supergroupJoinUpdate(
		&telego.ChatMemberLeft{User: user},
		&telego.ChatMemberMember{User: user},
	))

	ch, err := store.GetByUser(context.Background(), 200, 100)
	if err != nil {
		t.Fatalf("challenge not stored after join: %v", err)
	}

	// Build the exact callback_data the keyboard produced and feed it back.
	data := "cap:ans:" + ch.ID + ":" + strconv.Itoa(ch.CorrectAnswer)
	cq := telego.CallbackQuery{
		ID:   "cb1",
		From: user,
		Data: data,
		Message: &telego.Message{
			MessageID: ch.MessageID,
			Chat:      telego.Chat{ID: -200, Type: telego.ChatTypeSupergroup},
		},
	}
	if err := cbH(&th.Context{}, cq); err != nil {
		t.Fatalf("callback handler: %v", err)
	}

	// Resolved: challenge gone, message edited to the solved stamp, welcome animation posted.
	if _, err := store.Get(context.Background(), ch.ID); err != captcha.ErrNotFound {
		t.Fatalf("challenge must be cleared after a correct answer, got err=%v", err)
	}
	if txt := lastEditText(api); txt != text.MsgCaptchaSolved {
		t.Fatalf("expected solved stamp %q, got %q", text.MsgCaptchaSolved, txt)
	}
	waitForSendAnimation(t, api)
	if cap := lastAnimationCaption(api); !strings.Contains(cap, "Добро пожаловать") {
		t.Fatalf("expected welcome animation caption, got %q", cap)
	}
}

func TestCaptchaCallbackPredicate(t *testing.T) {
	t.Parallel()
	pred := captchaCallbackPredicate()

	sg := telego.Update{CallbackQuery: &telego.CallbackQuery{
		Data:    "cap:ans:abc:5",
		Message: &telego.Message{Chat: telego.Chat{Type: telego.ChatTypeSupergroup}},
	}}
	if !pred(context.Background(), sg) {
		t.Fatal("supergroup cap: callback must match")
	}

	// Wrong namespace in a supergroup -> no match (falls through to dispatcher).
	other := telego.Update{CallbackQuery: &telego.CallbackQuery{
		Data:    "v1:apply:abc",
		Message: &telego.Message{Chat: telego.Chat{Type: telego.ChatTypeSupergroup}},
	}}
	if pred(context.Background(), other) {
		t.Fatal("non-cap callback must NOT match")
	}

	// cap: in a private chat -> no match (captcha is supergroup-only).
	dm := telego.Update{CallbackQuery: &telego.CallbackQuery{
		Data:    "cap:ans:abc:5",
		Message: &telego.Message{Chat: telego.Chat{Type: telego.ChatTypePrivate}},
	}}
	if pred(context.Background(), dm) {
		t.Fatal("private-chat cap: callback must NOT match")
	}
}
