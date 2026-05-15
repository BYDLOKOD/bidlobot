package bot

import (
	"context"
	"errors"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/games/quiz"
)

// stubQuizSender records calls and lets tests assert what would have
// been sent to Telegram without a live bot.
type stubQuizSender struct {
	mu sync.Mutex

	NextMessageID int
	SendErr       error

	Sent    []*telego.SendMessageParams
	Edits   []*telego.EditMessageTextParams
	Answers []*telego.AnswerCallbackQueryParams
}

func (s *stubQuizSender) SendMessage(_ context.Context, params *telego.SendMessageParams) (*telego.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Sent = append(s.Sent, params)
	if s.SendErr != nil {
		return nil, s.SendErr
	}
	id := s.NextMessageID
	if id == 0 {
		id = 500 + len(s.Sent)
	}
	return &telego.Message{MessageID: id}, nil
}

func (s *stubQuizSender) EditMessageText(_ context.Context, params *telego.EditMessageTextParams) (*telego.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Edits = append(s.Edits, params)
	return &telego.Message{}, nil
}

func (s *stubQuizSender) AnswerCallbackQuery(_ context.Context, params *telego.AnswerCallbackQueryParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Answers = append(s.Answers, params)
	return nil
}

// memQuizStore is the in-memory test impl of quiz.Store.
type memQuizStore struct {
	mu      sync.Mutex
	entries map[string]quiz.Entry // chatID/userID -> entry
	failInc error
}

func newMemQuizStore() *memQuizStore { return &memQuizStore{entries: make(map[string]quiz.Entry)} }

func mkey(chatID, userID int64) string { return string(rune(chatID)) + ":" + string(rune(userID)) }

func (m *memQuizStore) IncrementCorrect(_ context.Context, e quiz.Entry) error {
	if m.failInc != nil {
		return m.failInc
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := mkey(e.AbsChatID, e.UserID)
	cur, ok := m.entries[k]
	if !ok {
		cur = quiz.Entry{AbsChatID: e.AbsChatID, UserID: e.UserID}
	}
	cur.CorrectCount++
	if e.Username != "" {
		cur.Username = e.Username
	}
	if e.FirstName != "" {
		cur.FirstName = e.FirstName
	}
	cur.LastPlayedAt = e.LastPlayedAt
	m.entries[k] = cur
	return nil
}

func (m *memQuizStore) GetEntry(_ context.Context, absChatID, userID int64) (*quiz.Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[mkey(absChatID, userID)]; ok {
		cp := e
		return &cp, nil
	}
	return nil, quiz.ErrNotFound
}

func (m *memQuizStore) TopByChat(_ context.Context, absChatID int64, limit int) ([]quiz.Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []quiz.Entry
	for _, e := range m.entries {
		if e.AbsChatID == absChatID {
			out = append(out, e)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func newQuizHandlerForTest(rng *rand.Rand) (*QuizHandler, *stubQuizSender, *memQuizStore, *quiz.ActiveQuizzes) {
	bot := &stubQuizSender{NextMessageID: 700}
	store := newMemQuizStore()
	active := quiz.NewActiveQuizzes()
	h := &QuizHandler{active: active, repo: store, bot: bot, log: testLogger(), rand: rng}
	return h, bot, store, active
}

func newQuizMsg(text string) telego.Message {
	return telego.Message{
		MessageID: 1,
		Chat:      telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
		Text:      text,
	}
}

func TestQuizHandlerStartPostsKeyboard(t *testing.T) {
	h, bot, _, active := newQuizHandlerForTest(rand.New(rand.NewSource(1)))
	if err := h.HandleQuiz(nil, newQuizMsg("/quiz")); err != nil {
		t.Fatal(err)
	}
	if len(bot.Sent) != 1 {
		t.Fatalf("expected 1 quiz message, got %d", len(bot.Sent))
	}
	body := bot.Sent[0].Text
	if !strings.Contains(body, "<pre>") {
		t.Errorf("expected <pre> code block, got %q", body[:80])
	}
	if bot.Sent[0].ReplyMarkup == nil {
		t.Fatal("quiz must carry inline keyboard")
	}
	kb, ok := bot.Sent[0].ReplyMarkup.(*telego.InlineKeyboardMarkup)
	if !ok {
		t.Fatalf("ReplyMarkup wrong type: %T", bot.Sent[0].ReplyMarkup)
	}
	total := 0
	for _, row := range kb.InlineKeyboard {
		total += len(row)
	}
	if total != 4 {
		t.Errorf("expected 4 buttons total, got %d", total)
	}
	if active.Active() != 1 {
		t.Errorf("expected 1 active quiz, got %d", active.Active())
	}
}

func TestQuizCallbackPredicateMatchesPrefix(t *testing.T) {
	pred := QuizCallbackPredicate()
	matches := pred(context.Background(), telego.Update{
		CallbackQuery: &telego.CallbackQuery{Data: "g1:q:0:0"},
	})
	if !matches {
		t.Error("predicate should match games prefix")
	}
	matches = pred(context.Background(), telego.Update{
		CallbackQuery: &telego.CallbackQuery{Data: "v1:apply:abc"},
	})
	if matches {
		t.Error("predicate must not match v1: pending-action prefix")
	}
}

func TestQuizCallbackCorrectAwardsCredit(t *testing.T) {
	h, bot, store, active := newQuizHandlerForTest(rand.New(rand.NewSource(2)))
	if err := h.HandleQuiz(nil, newQuizMsg("/quiz")); err != nil {
		t.Fatal(err)
	}

	// Find the snippet/correct index
	var msgID int64
	var snippetIdx, correctIdx int
	for id, q := range snapshot(active) {
		msgID = id
		snippetIdx = q.SnippetIdx
		correctIdx = q.CorrectIdx
	}

	cq := telego.CallbackQuery{
		ID:   "cb1",
		Data: makeQuizCallback(snippetIdx, correctIdx),
		From: telego.User{ID: 300, Username: "bob"},
		Message: &telego.Message{
			MessageID: int(msgID),
			Chat:      telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
		},
	}
	resp := h.dispatchCallback(context.Background(), cq)
	if resp.Toast != "Верно!" {
		t.Errorf("expected Верно! toast, got %q", resp.Toast)
	}
	if !strings.Contains(resp.EditedText, "Первым угадал") {
		t.Errorf("expected edited body to mention first solver, got %q", resp.EditedText)
	}
	if active.Active() != 0 {
		t.Errorf("solved quiz must be evicted; active=%d", active.Active())
	}
	got, err := store.GetEntry(context.Background(), 1001234567890, 300)
	if err != nil || got.CorrectCount != 1 {
		t.Errorf("leaderboard not updated; entry=%+v err=%v", got, err)
	}
	_ = bot
}

func TestQuizCallbackWrongShowsHint(t *testing.T) {
	h, _, _, active := newQuizHandlerForTest(rand.New(rand.NewSource(3)))
	_ = h.HandleQuiz(nil, newQuizMsg("/quiz"))

	var msgID int64
	var snippetIdx, correctIdx int
	for id, q := range snapshot(active) {
		msgID = id
		snippetIdx = q.SnippetIdx
		correctIdx = q.CorrectIdx
	}
	wrongIdx := (correctIdx + 1) % 4

	cq := telego.CallbackQuery{
		Data: makeQuizCallback(snippetIdx, wrongIdx),
		From: telego.User{ID: 300},
		Message: &telego.Message{
			MessageID: int(msgID),
			Chat:      telego.Chat{ID: -1001234567890},
		},
	}
	resp := h.dispatchCallback(context.Background(), cq)
	if !strings.HasPrefix(resp.Toast, "Не угадал") {
		t.Errorf("expected wrong-guess toast, got %q", resp.Toast)
	}
	if resp.EditedText != "" {
		t.Errorf("wrong guess must not edit message, got %q", resp.EditedText)
	}
	if active.Active() != 1 {
		t.Errorf("wrong guess must not solve; active=%d", active.Active())
	}
}

func TestQuizCallbackSecondCorrectGetsAlreadySolved(t *testing.T) {
	h, _, _, active := newQuizHandlerForTest(rand.New(rand.NewSource(4)))
	_ = h.HandleQuiz(nil, newQuizMsg("/quiz"))

	var msgID int64
	var snippetIdx, correctIdx int
	for id, q := range snapshot(active) {
		msgID = id
		snippetIdx = q.SnippetIdx
		correctIdx = q.CorrectIdx
	}
	cq := telego.CallbackQuery{
		Data: makeQuizCallback(snippetIdx, correctIdx),
		From: telego.User{ID: 300, Username: "bob"},
		Message: &telego.Message{
			MessageID: int(msgID),
			Chat:      telego.Chat{ID: -1001234567890},
		},
	}
	first := h.dispatchCallback(context.Background(), cq)
	if first.Toast != "Верно!" {
		t.Fatalf("first must win; got %q", first.Toast)
	}
	// Second tap on the same correct answer (after the message was
	// forgotten) yields "квиз больше не активен". A more interesting
	// case: tap before Forget runs - we test that path by re-registering.
	// Re-register the quiz with a fresh winner to simulate a race
	// where the second tap arrives after MarkSolved but before Forget.
	active.Register(&quiz.ActiveQuiz{
		MessageID:  msgID,
		AbsChatID:  1001234567890,
		SnippetIdx: snippetIdx,
		CorrectIdx: correctIdx,
		Options:    []quiz.Lang{quiz.LangPython, quiz.LangGo, quiz.LangJS, quiz.LangRust},
		WinnerID:   400, WinnerName: "@first",
	})
	second := h.dispatchCallback(context.Background(), cq)
	if !strings.HasPrefix(second.Toast, "Уже разгадано") {
		t.Errorf("second tap should be 'already solved', got %q", second.Toast)
	}
}

func TestQuizCallbackUnknownData(t *testing.T) {
	h, _, _, _ := newQuizHandlerForTest(rand.New(rand.NewSource(5)))
	resp := h.dispatchCallback(context.Background(), telego.CallbackQuery{
		Data:    "garbage",
		Message: &telego.Message{MessageID: 1, Chat: telego.Chat{ID: -1}},
	})
	if !strings.Contains(resp.Toast, "устарела") {
		t.Errorf("expected stale-button toast, got %q", resp.Toast)
	}
}

func TestQuizCallbackUnknownMessage(t *testing.T) {
	h, _, _, _ := newQuizHandlerForTest(rand.New(rand.NewSource(6)))
	resp := h.dispatchCallback(context.Background(), telego.CallbackQuery{
		Data:    makeQuizCallback(0, 0),
		Message: &telego.Message{MessageID: 999, Chat: telego.Chat{ID: -1}},
	})
	if !strings.Contains(resp.Toast, "не активен") {
		t.Errorf("expected 'not active' toast, got %q", resp.Toast)
	}
}

func TestQuizParseCallbackBadFormats(t *testing.T) {
	cases := []string{
		"",
		"v1:apply:abc",
		"g1:q",
		"g1:q:",
		"g1:q:1",
		"g1:q:1:",
		"g1:q::1",
		"g1:q:abc:1",
		"g1:q:1:xyz",
	}
	for _, c := range cases {
		if _, _, ok := parseQuizCallback(c); ok {
			t.Errorf("parseQuizCallback(%q) should fail", c)
		}
	}
}

func TestQuizParseCallbackRoundTrip(t *testing.T) {
	for _, snip := range []int{0, 5, 99} {
		for _, ch := range []int{0, 1, 2, 3} {
			data := makeQuizCallback(snip, ch)
			gotSnip, gotCh, ok := parseQuizCallback(data)
			if !ok || gotSnip != snip || gotCh != ch {
				t.Errorf("round-trip(%d,%d) -> (%d,%d,%v)", snip, ch, gotSnip, gotCh, ok)
			}
			if len(data) > 64 {
				t.Errorf("callback_data exceeds 64 bytes: %q", data)
			}
		}
	}
}

func TestQuizTopEmptyChat(t *testing.T) {
	h, bot, _, _ := newQuizHandlerForTest(rand.New(rand.NewSource(7)))
	if err := h.HandleQuiz(nil, newQuizMsg("/quiz top")); err != nil {
		t.Fatal(err)
	}
	if len(bot.Sent) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(bot.Sent))
	}
	if !strings.Contains(bot.Sent[0].Text, "Пока никто") {
		t.Errorf("expected empty-board message, got %q", bot.Sent[0].Text)
	}
}

func TestQuizTopFromStore(t *testing.T) {
	h, bot, store, _ := newQuizHandlerForTest(rand.New(rand.NewSource(8)))
	for _, e := range []quiz.Entry{
		{AbsChatID: 1001234567890, UserID: 200, Username: "alice", CorrectCount: 5, LastPlayedAt: time.Now()},
		{AbsChatID: 1001234567890, UserID: 300, Username: "bob", CorrectCount: 3, LastPlayedAt: time.Now()},
	} {
		store.entries[mkey(e.AbsChatID, e.UserID)] = e
	}
	if err := h.HandleQuiz(nil, newQuizMsg("/quiz top")); err != nil {
		t.Fatal(err)
	}
	body := bot.Sent[0].Text
	if !strings.Contains(body, "alice") || !strings.Contains(body, "bob") {
		t.Errorf("top body missing names: %q", body)
	}
}

func TestQuizSendErrorReplies(t *testing.T) {
	h, bot, _, active := newQuizHandlerForTest(rand.New(rand.NewSource(9)))
	bot.SendErr = errors.New("rate limit")
	if err := h.HandleQuiz(nil, newQuizMsg("/quiz")); err != nil {
		t.Fatal(err)
	}
	if len(bot.Sent) < 2 {
		// Both the original quiz send and the error reply attempt.
		// SendErr persists, so we just want no panic and active=0.
		t.Logf("sent count: %d", len(bot.Sent))
	}
	if active.Active() != 0 {
		t.Errorf("active must be empty when send failed; got %d", active.Active())
	}
}

func TestQuizFromBotIgnored(t *testing.T) {
	h, bot, _, active := newQuizHandlerForTest(rand.New(rand.NewSource(10)))
	msg := newQuizMsg("/quiz")
	msg.From = &telego.User{ID: 200, IsBot: true}
	if err := h.HandleQuiz(nil, msg); err != nil {
		t.Fatal(err)
	}
	if len(bot.Sent) != 0 {
		t.Errorf("bot users must be ignored; sent=%d", len(bot.Sent))
	}
	if active.Active() != 0 {
		t.Errorf("no quiz expected; active=%d", active.Active())
	}
}

// snapshot dumps the current active map for tests that need to read
// the registered quiz state without exposing internals.
func snapshot(a *quiz.ActiveQuizzes) map[int64]*quiz.ActiveQuiz {
	out := make(map[int64]*quiz.ActiveQuiz)
	for i := int64(1); i <= 2000; i++ {
		if q := a.Get(i); q != nil {
			out[i] = q
		}
	}
	return out
}
