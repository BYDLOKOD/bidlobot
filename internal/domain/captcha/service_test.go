package captcha

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/testutil"
	"github.com/veschin/bidlobot/internal/text"
)

func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeStore is an in-memory captcha.Store for unit tests.
type fakeStore struct {
	mu   sync.Mutex
	byID map[string]Challenge
}

func newFakeStore() *fakeStore { return &fakeStore{byID: map[string]Challenge{}} }

func (s *fakeStore) Create(_ context.Context, c Challenge) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[c.ID] = c
	return nil
}

func (s *fakeStore) Get(_ context.Context, id string) (*Challenge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.byID[id]; ok {
		return &c, nil
	}
	return nil, ErrNotFound
}

func (s *fakeStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, id)
	return nil
}

func (s *fakeStore) GetByUser(_ context.Context, absChatID, userID int64) (*Challenge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.byID {
		if c.AbsChatID == absChatID && c.UserID == userID {
			return &c, nil
		}
	}
	return nil, ErrNotFound
}

func (s *fakeStore) ListExpired(_ context.Context, now time.Time) ([]Challenge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Challenge
	for _, c := range s.byID {
		if c.ExpiresAt.Before(now) {
			out = append(out, c)
		}
	}
	return out, nil
}

func newTestService(t *testing.T) (*Service, *fakeStore, *testutil.MockAPI) {
	t.Helper()
	api := testutil.NewMockAPI()
	store := newFakeStore()
	svc := NewService(store, api, testLogger(t), 10*time.Minute)
	return svc, store, api
}

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

func TestOnJoinPostsCaptchaAndMutes(t *testing.T) {
	t.Parallel()
	svc, store, api := newTestService(t)
	now := time.Now().UTC()

	err := svc.OnJoin(context.Background(),
		telego.User{ID: 100, Username: "alice", FirstName: "Alice"},
		-200, 200, now)
	if err != nil {
		t.Fatalf("OnJoin: %v", err)
	}

	// One public message with a 4-button keyboard, all cap:-prefixed.
	if got := len(api.Messages); got != 1 {
		t.Fatalf("expected 1 sent message, got %d", got)
	}
	kb := api.Messages[0].Keyboard
	if kb == nil || len(kb.InlineKeyboard) == 0 {
		t.Fatalf("expected an inline keyboard")
	}
	btns := kb.InlineKeyboard[0]
	if len(btns) != 4 {
		t.Fatalf("expected 4 answer buttons, got %d", len(btns))
	}
	for _, b := range btns {
		if !strings.HasPrefix(b.CallbackData, "cap:ans:") {
			t.Fatalf("callback_data must be cap:-prefixed, got %q", b.CallbackData)
		}
	}

	// The newcomer must be muted.
	if api.CallCount("RestrictChatMember") == 0 {
		t.Fatal("expected a RestrictChatMember (mute) call")
	}

	// Mute MUST precede SendMessage (no spam window).
	var sawMute, sawMessage bool
	for _, c := range api.Calls {
		switch c.Method {
		case "RestrictChatMember":
			sawMute = true
		case "SendMessage":
			if !sawMute {
				t.Fatal("RestrictChatMember (mute) must precede SendMessage")
			}
			sawMessage = true
		}
	}
	if !sawMessage {
		t.Fatal("SendMessage not found in API calls")
	}

	// The challenge is persisted with the posted message id.
	if n := countStore(store); n != 1 {
		t.Fatalf("expected 1 stored challenge, got %d", n)
	}
	got, gerr := store.GetByUser(context.Background(), 200, 100)
	if gerr != nil {
		t.Fatalf("GetByUser: %v", gerr)
	}
	if got.MessageID != 1 {
		t.Fatalf("stored MessageID must be the sent message id (1), got %d", got.MessageID)
	}
}

func TestOnJoinDropsStaleChallengeOnRejoin(t *testing.T) {
	t.Parallel()
	svc, store, _ := newTestService(t)
	now := time.Now().UTC()

	// First join.
	if err := svc.OnJoin(context.Background(),
		telego.User{ID: 100, Username: "alice"}, -200, 200, now); err != nil {
		t.Fatal(err)
	}
	// Second join for the same user: the old challenge must be replaced.
	if err := svc.OnJoin(context.Background(),
		telego.User{ID: 100, Username: "alice"}, -200, 200, now); err != nil {
		t.Fatal(err)
	}
	if n := countStore(store); n != 1 {
		t.Fatalf("rejoin must leave exactly one challenge, got %d", n)
	}
}

func TestOnAnswerCorrectClearsAndUnmutes(t *testing.T) {
	t.Parallel()
	svc, store, api := newTestService(t)
	now := time.Now().UTC()

	if err := svc.OnJoin(context.Background(),
		telego.User{ID: 100, FirstName: "Alice"}, -200, 200, now); err != nil {
		t.Fatal(err)
	}
	ch, _ := store.GetByUser(context.Background(), 200, 100)

	// Join itself mutes once; count after join as the baseline.
	muteAfterJoin := api.CallCount("RestrictChatMember")

	q := telego.CallbackQuery{ID: "q1", From: telego.User{ID: 100}}
	if err := svc.OnAnswer(context.Background(), q, ch.ID, ch.CorrectAnswer); err != nil {
		t.Fatalf("OnAnswer: %v", err)
	}

	// Challenge cleared.
	if _, err := store.Get(context.Background(), ch.ID); err != ErrNotFound {
		t.Fatalf("challenge must be deleted on correct answer, got err=%v", err)
	}
	// Message edited to the solved stamp (sync); welcome animation posted async.
	if txt := lastEditText(api); txt != text.MsgCaptchaSolved {
		t.Fatalf("expected solved stamp %q, got %q", text.MsgCaptchaSolved, txt)
	}
	waitForSendAnimation(t, api)
	if cap := lastAnimationCaption(api); !strings.Contains(cap, "Добро пожаловать") {
		t.Fatalf("expected welcome animation caption, got %q", cap)
	}
	// Unmute: a second RestrictChatMember beyond the join-time mute.
	if got, want := api.CallCount("RestrictChatMember"), muteAfterJoin+1; got != want {
		t.Fatalf("expected unmute (restrict count %d -> %d), got %d", muteAfterJoin, want, got)
	}
	// Callback answered (spinner cleared).
	if api.CallCount("AnswerCallbackQuery") == 0 {
		t.Fatal("expected AnswerCallbackQuery call")
	}
}

func TestOnAnswerWrongKicksAndDeletes(t *testing.T) {
	t.Parallel()
	svc, store, api := newTestService(t)
	now := time.Now().UTC()

	if err := svc.OnJoin(context.Background(),
		telego.User{ID: 100, FirstName: "Alice"}, -200, 200, now); err != nil {
		t.Fatal(err)
	}
	ch, _ := store.GetByUser(context.Background(), 200, 100)

	wrong := ch.CorrectAnswer + 100 // guaranteed wrong
	q := telego.CallbackQuery{ID: "q1", From: telego.User{ID: 100}}
	if err := svc.OnAnswer(context.Background(), q, ch.ID, wrong); err != nil {
		t.Fatalf("OnAnswer: %v", err)
	}

	// Challenge MUST be deleted after a wrong-answer kick.
	if _, err := store.Get(context.Background(), ch.ID); err != ErrNotFound {
		t.Fatalf("challenge must be deleted after wrong-answer kick, got err=%v", err)
	}
	// Kick: ban + unban.
	if api.CallCount("BanChatMember") != 1 {
		t.Fatalf("expected 1 BanChatMember (kick), got %d", api.CallCount("BanChatMember"))
	}
	if api.CallCount("UnbanChatMember") != 1 {
		t.Fatalf("expected 1 UnbanChatMember (rejoinable), got %d", api.CallCount("UnbanChatMember"))
	}
	// Message edited to kicked.
	if api.CallCount("EditMessageText") != 1 {
		t.Fatalf("expected 1 EditMessageText (kicked notice), got %d", api.CallCount("EditMessageText"))
	}
	// Toast.
	if api.CallCount("AnswerCallbackQuery") == 0 {
		t.Fatal("expected an AnswerCallbackQuery (toast) on wrong answer")
	}
}

func TestOnAnswerWrongUserRejected(t *testing.T) {
	t.Parallel()
	svc, store, api := newTestService(t)
	now := time.Now().UTC()

	if err := svc.OnJoin(context.Background(),
		telego.User{ID: 100, FirstName: "Alice"}, -200, 200, now); err != nil {
		t.Fatal(err)
	}
	ch, _ := store.GetByUser(context.Background(), 200, 100)

	// A different user taps the button.
	q := telego.CallbackQuery{ID: "q2", From: telego.User{ID: 999}}
	if err := svc.OnAnswer(context.Background(), q, ch.ID, ch.CorrectAnswer); err != nil {
		t.Fatalf("OnAnswer: %v", err)
	}

	// Challenge survives; the tap was not for them.
	if _, err := store.Get(context.Background(), ch.ID); err != nil {
		t.Fatalf("challenge must survive a wrong-user tap, got err=%v", err)
	}
	// The not-yours toast must fire (otherwise the button spinner hangs).
	if api.CallCount("AnswerCallbackQuery") == 0 {
		t.Fatal("wrong-user tap must answer the callback with a not-yours toast")
	}
}

func TestOnAnswerExpiredRejected(t *testing.T) {
	t.Parallel()
	svc, _, api := newTestService(t)

	// No challenge in the store -> ErrNotFound path.
	q := telego.CallbackQuery{ID: "q3", From: telego.User{ID: 100}}
	if err := svc.OnAnswer(context.Background(), q, "deadbeefdeadbeef", 5); err != nil {
		t.Fatalf("OnAnswer on missing challenge must not error, got %v", err)
	}
	if api.CallCount("AnswerCallbackQuery") == 0 {
		t.Fatal("expired/missing challenge must still answer the callback")
	}
}

func TestSweepKicksExpired(t *testing.T) {
	t.Parallel()
	svc, store, api := newTestService(t)

	// Seed an already-expired challenge directly.
	ch := Generate(100, 200, time.Now().Add(-time.Hour), 10*time.Minute)
	ch.Username = "alice"
	ch.MessageID = 42
	_ = store.Create(context.Background(), ch)
	// The captcha mute makes the user "restricted"; the kick MUST still
	// fire on that status (a regression that re-adds it to the skip-list
	// would let non-answering users stay).
	api.ChatMembers["200:100"] = "restricted"

	if err := svc.Sweep(context.Background(), time.Now().UTC()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	// ban + unban (the kick sequence).
	if api.CallCount("BanChatMember") != 1 {
		t.Fatalf("expected 1 BanChatMember, got %d", api.CallCount("BanChatMember"))
	}
	if api.CallCount("UnbanChatMember") != 1 {
		t.Fatalf("expected 1 UnbanChatMember, got %d", api.CallCount("UnbanChatMember"))
	}
	// Message rewritten to the kicked notice.
	if txt := lastEditText(api); !strings.Contains(txt, "кикнут") {
		t.Fatalf("expected kicked edit, got %q", txt)
	}
	// Challenge deleted after the sweep.
	if _, err := store.Get(context.Background(), ch.ID); err != ErrNotFound {
		t.Fatalf("expired challenge must be deleted after sweep, got err=%v", err)
	}
}
func TestSweepSkipsAlreadyLeft(t *testing.T) {
	t.Parallel()
	svc, store, api := newTestService(t)

	ch := Generate(100, 200, time.Now().Add(-time.Hour), 10*time.Minute)
	ch.MessageID = 42
	_ = store.Create(context.Background(), ch)
	// The user already left on their own -> kick must be a no-op.
	api.ChatMembers["200:100"] = "left"

	_ = svc.Sweep(context.Background(), time.Now().UTC())

	if api.CallCount("BanChatMember") != 0 {
		t.Fatalf("must not ban a user who already left, got %d bans", api.CallCount("BanChatMember"))
	}
	// Still cleaned up (edit + delete happen regardless).
	if _, err := store.Get(context.Background(), ch.ID); err != ErrNotFound {
		t.Fatalf("challenge must still be deleted, got err=%v", err)
	}
}

func countStore(s *fakeStore) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byID)
}
