package bot

import (
	"context"
	"math/rand"
	"strings"
	"testing"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/games/quiz"
)

// newTriviaHandlerForTest reuses stubQuizSender + memQuizStore (defined
// in games_quiz_test.go, same package) since trivia shares the quiz
// leaderboard store contract.
func newTriviaHandlerForTest(rng *rand.Rand) (*TriviaHandler, *stubQuizSender, *memQuizStore) {
	bot := &stubQuizSender{NextMessageID: 800}
	store := newMemQuizStore()
	h := &TriviaHandler{
		active: newActiveTrivias(),
		repo:   store,
		bot:    bot,
		log:    testLogger(),
		rand:   rng,
	}
	return h, bot, store
}

func newTriviaMsg(text string) telego.Message {
	return telego.Message{
		MessageID: 1,
		Chat:      telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
		Text:      text,
	}
}

// triviaSnapshot copies the active map for safe iteration in tests.
func triviaSnapshot(a *activeTrivias) map[int64]*activeTrivia {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[int64]*activeTrivia, len(a.byMsg))
	for k, v := range a.byMsg {
		out[k] = v
	}
	return out
}

func TestTriviaHandlerStartPostsKeyboard(t *testing.T) {
	h, bot, _ := newTriviaHandlerForTest(rand.New(rand.NewSource(1)))
	if err := h.HandleTrivia(nil, newTriviaMsg("/trivia")); err != nil {
		t.Fatal(err)
	}
	if len(bot.Sent) != 1 {
		t.Fatalf("expected 1 trivia message, got %d", len(bot.Sent))
	}
	body := bot.Sent[0].Text
	if !strings.Contains(body, "IT-викторина") {
		t.Errorf("expected trivia header, got %q", body)
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
	if h.active.active() != 1 {
		t.Errorf("expected 1 active trivia, got %d", h.active.active())
	}
}

func TestTriviaCallbackPredicateMatchesOnlyTriviaVerb(t *testing.T) {
	pred := TriviaCallbackPredicate()
	if !pred(context.Background(), telego.Update{
		CallbackQuery: &telego.CallbackQuery{Data: "g1:t:0:0"},
	}) {
		t.Error("predicate should match the trivia verb g1:t:")
	}
	// Must NOT swallow quiz callbacks (different verb) or pending-action.
	if pred(context.Background(), telego.Update{
		CallbackQuery: &telego.CallbackQuery{Data: "g1:q:0:0"},
	}) {
		t.Error("trivia predicate must not match the quiz verb g1:q:")
	}
	if pred(context.Background(), telego.Update{
		CallbackQuery: &telego.CallbackQuery{Data: "v1:apply:abc"},
	}) {
		t.Error("trivia predicate must not match v1: pending actions")
	}
}

func TestTriviaCallbackCorrectAwardsCredit(t *testing.T) {
	h, _, store := newTriviaHandlerForTest(rand.New(rand.NewSource(2)))
	if err := h.HandleTrivia(nil, newTriviaMsg("/trivia")); err != nil {
		t.Fatal(err)
	}

	var msgID int64
	var triviaIdx, correctIdx int
	for id, q := range triviaSnapshot(h.active) {
		msgID = id
		triviaIdx = q.TriviaIdx
		correctIdx = q.CorrectIdx
	}

	cq := telego.CallbackQuery{
		ID:   "cb1",
		Data: makeTriviaCallback(triviaIdx, correctIdx),
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
	if !strings.Contains(resp.EditedText, "Первым ответил") {
		t.Errorf("edited body should mention first solver, got %q", resp.EditedText)
	}
	if h.active.active() != 0 {
		t.Errorf("solved trivia must be evicted; active=%d", h.active.active())
	}
	got, err := store.GetEntry(context.Background(), 1001234567890, 300)
	if err != nil || got.CorrectCount != 1 {
		t.Errorf("leaderboard not updated; entry=%+v err=%v", got, err)
	}
}

func TestTriviaCallbackWrongShowsHint(t *testing.T) {
	h, _, _ := newTriviaHandlerForTest(rand.New(rand.NewSource(3)))
	if err := h.HandleTrivia(nil, newTriviaMsg("/trivia")); err != nil {
		t.Fatal(err)
	}

	var msgID int64
	var triviaIdx, correctIdx int
	for id, q := range triviaSnapshot(h.active) {
		msgID = id
		triviaIdx = q.TriviaIdx
		correctIdx = q.CorrectIdx
	}
	wrongIdx := (correctIdx + 1) % 4

	cq := telego.CallbackQuery{
		Data: makeTriviaCallback(triviaIdx, wrongIdx),
		From: telego.User{ID: 300},
		Message: &telego.Message{
			MessageID: int(msgID),
			Chat:      telego.Chat{ID: -1001234567890},
		},
	}
	resp := h.dispatchCallback(context.Background(), cq)
	if !strings.HasPrefix(resp.Toast, "Не верно") {
		t.Errorf("expected wrong-answer toast, got %q", resp.Toast)
	}
	if resp.EditedText != "" {
		t.Errorf("wrong answer must not edit the message, got %q", resp.EditedText)
	}
	if h.active.active() != 1 {
		t.Errorf("wrong answer must not solve; active=%d", h.active.active())
	}
}

func TestTriviaCallbackSecondCorrectGetsAlreadyAnswered(t *testing.T) {
	h, _, _ := newTriviaHandlerForTest(rand.New(rand.NewSource(4)))
	if err := h.HandleTrivia(nil, newTriviaMsg("/trivia")); err != nil {
		t.Fatal(err)
	}
	var msgID int64
	var triviaIdx, correctIdx int
	for id, q := range triviaSnapshot(h.active) {
		msgID = id
		triviaIdx = q.TriviaIdx
		correctIdx = q.CorrectIdx
	}
	base := telego.CallbackQuery{
		Data: makeTriviaCallback(triviaIdx, correctIdx),
		From: telego.User{ID: 300, Username: "bob"},
		Message: &telego.Message{
			MessageID: int(msgID),
			Chat:      telego.Chat{ID: -1001234567890},
		},
	}
	if resp := h.dispatchCallback(context.Background(), base); resp.Toast != "Верно!" {
		t.Fatalf("first correct should win, got %q", resp.Toast)
	}
	// A second tap (anyone) after solve: the message is no longer active.
	second := base
	second.From = telego.User{ID: 400, Username: "carol"}
	resp := h.dispatchCallback(context.Background(), second)
	if !strings.Contains(resp.Toast, "больше не активна") {
		t.Errorf("post-solve tap should report inactive, got %q", resp.Toast)
	}
}

func TestTriviaCallbackUnknownData(t *testing.T) {
	h, _, _ := newTriviaHandlerForTest(rand.New(rand.NewSource(5)))
	resp := h.dispatchCallback(context.Background(), telego.CallbackQuery{
		Data:    "garbage",
		Message: &telego.Message{MessageID: 1},
	})
	if !strings.Contains(resp.Toast, "устарела") {
		t.Errorf("garbage data should yield a stale-button toast, got %q", resp.Toast)
	}
}

func TestTriviaTopEmptyAndPopulated(t *testing.T) {
	h, bot, store := newTriviaHandlerForTest(rand.New(rand.NewSource(6)))
	if err := h.HandleTrivia(nil, newTriviaMsg("/trivia top")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bot.Sent[len(bot.Sent)-1].Text, "никто не ответил") {
		t.Errorf("empty leaderboard expected, got %q", bot.Sent[len(bot.Sent)-1].Text)
	}
	// Seed an entry and re-query.
	if err := store.IncrementCorrect(context.Background(), quiz.Entry{
		AbsChatID: 1001234567890, UserID: 300, Username: "bob",
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.HandleTrivia(nil, newTriviaMsg("/trivia top")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bot.Sent[len(bot.Sent)-1].Text, "bob") {
		t.Errorf("leaderboard should list @bob, got %q", bot.Sent[len(bot.Sent)-1].Text)
	}
}

func TestTriviaHandlerIgnoresBotAndNilFrom(t *testing.T) {
	h, bot, _ := newTriviaHandlerForTest(rand.New(rand.NewSource(7)))
	m := newTriviaMsg("/trivia")
	m.From = nil
	if err := h.HandleTrivia(nil, m); err != nil {
		t.Fatal(err)
	}
	m2 := newTriviaMsg("/trivia")
	m2.From = &telego.User{ID: 9, IsBot: true}
	if err := h.HandleTrivia(nil, m2); err != nil {
		t.Fatal(err)
	}
	if len(bot.Sent) != 0 {
		t.Errorf("bot/nil sender must be ignored, got %d sends", len(bot.Sent))
	}
}
