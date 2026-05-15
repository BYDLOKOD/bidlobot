package bot

import (
	"context"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/games/hangman"
)

// stubHangmanSender records SendMessage params.
type stubHangmanSender struct {
	mu           sync.Mutex
	MessageCalls []*telego.SendMessageParams
}

func (s *stubHangmanSender) SendMessage(_ context.Context, p *telego.SendMessageParams) (*telego.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.MessageCalls = append(s.MessageCalls, p)
	return &telego.Message{MessageID: 1000}, nil
}

func (s *stubHangmanSender) last() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.MessageCalls) == 0 {
		return ""
	}
	return s.MessageCalls[len(s.MessageCalls)-1].Text
}

func (s *stubHangmanSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.MessageCalls)
}

// memHangmanStore is an in-memory hangman.Store for handler tests.
type memHangmanStore struct {
	mu     sync.Mutex
	rounds map[int64]hangman.Round
}

func newMemHangmanStore() *memHangmanStore {
	return &memHangmanStore{rounds: make(map[int64]hangman.Round)}
}

func (m *memHangmanStore) GetRound(_ context.Context, absChatID int64) (*hangman.Round, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rounds[absChatID]
	if !ok {
		return nil, hangman.ErrNotFound
	}
	cp := r
	cp.Used = make(map[string]bool, len(r.Used))
	for k, v := range r.Used {
		cp.Used[k] = v
	}
	return &cp, nil
}

func (m *memHangmanStore) PutRound(_ context.Context, r hangman.Round) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := r
	cp.Used = make(map[string]bool, len(r.Used))
	for k, v := range r.Used {
		cp.Used[k] = v
	}
	m.rounds[r.AbsChatID] = cp
	return nil
}

func (m *memHangmanStore) DeleteRound(_ context.Context, absChatID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rounds, absChatID)
	return nil
}

func newHangmanTestHandler() (*HangmanHandler, *stubHangmanSender, *memHangmanStore) {
	store := newMemHangmanStore()
	svc := hangman.NewService(store, rand.New(rand.NewSource(1)), testLogger())
	sender := &stubHangmanSender{}
	return NewHangmanHandler(svc, sender, testLogger()), sender, store
}

func newHangmanMsg(text string) telego.Message {
	return telego.Message{
		MessageID: 1,
		Date:      int64(time.Now().Unix()),
		Chat:      telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
		Text:      text,
	}
}

// seedRound forces a known word so the guess-flow tests are
// deterministic regardless of the random pool.
func seedRound(t *testing.T, store *memHangmanStore, word string) {
	t.Helper()
	store.PutRound(context.Background(), hangman.Round{
		AbsChatID: 1001234567890,
		Word:      strings.ToUpper(word),
		Used:      map[string]bool{},
		Active:    true,
		StartedAt: time.Now().UTC(),
	})
}

func TestHangmanHandlerStartsRound(t *testing.T) {
	h, sender, store := newHangmanTestHandler()
	if err := h.HandleHangman(nil, newHangmanMsg("/hangman")); err != nil {
		t.Fatal(err)
	}
	if sender.count() != 1 {
		t.Fatalf("expected 1 reply, got %d", sender.count())
	}
	if !strings.Contains(sender.last(), "Виселица") {
		t.Errorf("start should render the board, got %q", sender.last())
	}
	r, _ := store.GetRound(context.Background(), 1001234567890)
	if r == nil || r.Word == "" {
		t.Errorf("round should be stored, got %+v", r)
	}
	// Fresh board: secret word must not appear in clear text.
	if strings.Contains(sender.last(), r.Word) {
		t.Errorf("fresh board must not leak the word %q in %q", r.Word, sender.last())
	}
}

func TestHangmanHandlerSecondStartShowsBoard(t *testing.T) {
	h, sender, _ := newHangmanTestHandler()
	if err := h.HandleHangman(nil, newHangmanMsg("/hangman")); err != nil {
		t.Fatal(err)
	}
	if err := h.HandleHangman(nil, newHangmanMsg("/hangman")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sender.last(), "Игра уже идёт") {
		t.Errorf("second /hangman should show the running board, got %q", sender.last())
	}
}

func TestHangmanHandlerHitMissWin(t *testing.T) {
	h, sender, store := newHangmanTestHandler()
	seedRound(t, store, "go")

	if err := h.HandleHangman(nil, newHangmanMsg("/hangman g")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sender.last(), "Есть буква") {
		t.Errorf("g should be a hit, got %q", sender.last())
	}

	if err := h.HandleHangman(nil, newHangmanMsg("/hangman z")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sender.last(), "Мимо") {
		t.Errorf("z should be a miss, got %q", sender.last())
	}

	if err := h.HandleHangman(nil, newHangmanMsg("/hangman o")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sender.last(), "отгадал слово") || !strings.Contains(sender.last(), "GO") {
		t.Errorf("o should complete the win, got %q", sender.last())
	}
	if _, err := store.GetRound(context.Background(), 1001234567890); err != hangman.ErrNotFound {
		t.Errorf("round should be deleted after the win, got %v", err)
	}
}

func TestHangmanHandlerLoss(t *testing.T) {
	h, sender, store := newHangmanTestHandler()
	seedRound(t, store, "go")
	for _, c := range []string{"a", "b", "c", "d", "e", "f"} {
		if err := h.HandleHangman(nil, newHangmanMsg("/hangman "+c)); err != nil {
			t.Fatalf("guess %q: %v", c, err)
		}
	}
	if !strings.Contains(sender.last(), "Виселица") || !strings.Contains(sender.last(), "GO") {
		t.Errorf("6 wrong should lose and reveal the word, got %q", sender.last())
	}
	if _, err := store.GetRound(context.Background(), 1001234567890); err != hangman.ErrNotFound {
		t.Errorf("round should be deleted after the loss, got %v", err)
	}
}

func TestHangmanHandlerRejectsMultiChar(t *testing.T) {
	h, sender, _ := newHangmanTestHandler()
	if err := h.HandleHangman(nil, newHangmanMsg("/hangman")); err != nil {
		t.Fatal(err)
	}
	if err := h.HandleHangman(nil, newHangmanMsg("/hangman ab")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sender.last(), "ровно одна буква") {
		t.Errorf("multi-char guess should get a hint, got %q", sender.last())
	}
}

func TestHangmanHandlerAlreadyUsed(t *testing.T) {
	h, sender, store := newHangmanTestHandler()
	seedRound(t, store, "golang")
	if err := h.HandleHangman(nil, newHangmanMsg("/hangman g")); err != nil {
		t.Fatal(err)
	}
	if err := h.HandleHangman(nil, newHangmanMsg("/hangman G")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sender.last(), "уже называли") {
		t.Errorf("repeated letter should be reported, got %q", sender.last())
	}
}

func TestHangmanHandlerCyrillic(t *testing.T) {
	h, sender, store := newHangmanTestHandler()
	seedRound(t, store, "горутина")
	if err := h.HandleHangman(nil, newHangmanMsg("/hangman г")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sender.last(), "Есть буква") {
		t.Errorf("cyrillic letter should hit cyrillic word, got %q", sender.last())
	}
}

func TestHangmanHandlerGuessNoRound(t *testing.T) {
	h, sender, _ := newHangmanTestHandler()
	if err := h.HandleHangman(nil, newHangmanMsg("/hangman a")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sender.last(), "Нет активной игры") {
		t.Errorf("guess with no round should hint to start, got %q", sender.last())
	}
}

func TestHangmanHandlerIgnoresBotAndNilFrom(t *testing.T) {
	h, sender, _ := newHangmanTestHandler()
	m := newHangmanMsg("/hangman")
	m.From = nil
	if err := h.HandleHangman(nil, m); err != nil {
		t.Fatal(err)
	}
	m2 := newHangmanMsg("/hangman")
	m2.From = &telego.User{ID: 9, IsBot: true}
	if err := h.HandleHangman(nil, m2); err != nil {
		t.Fatal(err)
	}
	if sender.count() != 0 {
		t.Errorf("bot/nil sender must be ignored, got %d", sender.count())
	}
}
