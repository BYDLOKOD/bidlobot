package bot

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/games/guess"
)

// stubGuessSender records SendMessage params for assertions.
type stubGuessSender struct {
	mu           sync.Mutex
	MessageCalls []*telego.SendMessageParams
}

func (s *stubGuessSender) SendMessage(_ context.Context, p *telego.SendMessageParams) (*telego.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.MessageCalls = append(s.MessageCalls, p)
	return &telego.Message{MessageID: 1000}, nil
}

func (s *stubGuessSender) last() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.MessageCalls) == 0 {
		return ""
	}
	return s.MessageCalls[len(s.MessageCalls)-1].Text
}

func (s *stubGuessSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.MessageCalls)
}

// memGuessStore is an in-memory guess.Store for handler tests.
type memGuessStore struct {
	mu     sync.Mutex
	rounds map[int64]guess.Round
	wins   map[string]guess.WinEntry
}

func newMemGuessStore() *memGuessStore {
	return &memGuessStore{
		rounds: make(map[int64]guess.Round),
		wins:   make(map[string]guess.WinEntry),
	}
}

func gWinKey(c, u int64) string {
	return string(rune(c&0xffff)) + "/" + string(rune(u&0xffff))
}

func (m *memGuessStore) GetRound(_ context.Context, absChatID int64) (*guess.Round, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rounds[absChatID]
	if !ok {
		return nil, guess.ErrNotFound
	}
	cp := r
	return &cp, nil
}

func (m *memGuessStore) PutRound(_ context.Context, r guess.Round) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rounds[r.AbsChatID] = r
	return nil
}

func (m *memGuessStore) DeleteRound(_ context.Context, absChatID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rounds, absChatID)
	return nil
}

func (m *memGuessStore) IncrementWin(_ context.Context, e guess.WinEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := gWinKey(e.AbsChatID, e.UserID)
	cur, ok := m.wins[k]
	if !ok {
		cur = guess.WinEntry{AbsChatID: e.AbsChatID, UserID: e.UserID}
	}
	cur.Wins++
	if e.Username != "" {
		cur.Username = e.Username
	}
	if e.FirstName != "" {
		cur.FirstName = e.FirstName
	}
	if !e.LastWonAt.IsZero() {
		cur.LastWonAt = e.LastWonAt
	}
	m.wins[k] = cur
	return nil
}

func (m *memGuessStore) TopWins(_ context.Context, absChatID int64, limit int) ([]guess.WinEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []guess.WinEntry
	for _, e := range m.wins {
		if e.AbsChatID == absChatID {
			all = append(all, e)
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Wins != all[j].Wins {
			return all[i].Wins > all[j].Wins
		}
		return all[i].LastWonAt.Before(all[j].LastWonAt)
	})
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

type fixedGuessRand struct{ v int }

func (f fixedGuessRand) Intn(int) int { return f.v }

func newGuessTestHandler(secretMinusMin int) (*GuessHandler, *stubGuessSender, *memGuessStore) {
	store := newMemGuessStore()
	svc := guess.NewService(store, fixedGuessRand{secretMinusMin}, testLogger())
	sender := &stubGuessSender{}
	return NewGuessHandler(svc, sender, testLogger()), sender, store
}

func newGuessMsg(text string) telego.Message {
	return telego.Message{
		MessageID: 1,
		Date:      int64(time.Now().Unix()),
		Chat:      telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
		Text:      text,
	}
}

func TestGuessHandlerStartsRound(t *testing.T) {
	h, sender, store := newGuessTestHandler(41) // secret 42
	if err := h.HandleGuess(nil, newGuessMsg("/guess")); err != nil {
		t.Fatal(err)
	}
	if sender.count() != 1 {
		t.Fatalf("expected 1 reply, got %d", sender.count())
	}
	if !strings.Contains(sender.last(), "Загадал число") {
		t.Errorf("start should announce a new round, got %q", sender.last())
	}
	r, _ := store.GetRound(context.Background(), 1001234567890)
	if r == nil || r.Secret != 42 {
		t.Errorf("round should be stored with secret 42, got %+v", r)
	}
}

func TestGuessHandlerSecondStartShowsStatus(t *testing.T) {
	h, sender, _ := newGuessTestHandler(41)
	if err := h.HandleGuess(nil, newGuessMsg("/guess")); err != nil {
		t.Fatal(err)
	}
	if err := h.HandleGuess(nil, newGuessMsg("/guess")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sender.last(), "Раунд уже идёт") {
		t.Errorf("second /guess should show status, got %q", sender.last())
	}
	if strings.Contains(sender.last(), "42") {
		t.Errorf("status must not leak the secret, got %q", sender.last())
	}
}

func TestGuessHandlerGuessFlowLowHighWin(t *testing.T) {
	h, sender, store := newGuessTestHandler(49) // secret 50
	if err := h.HandleGuess(nil, newGuessMsg("/guess")); err != nil {
		t.Fatal(err)
	}

	if err := h.HandleGuess(nil, newGuessMsg("/guess 25")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sender.last(), "мало") {
		t.Errorf("25 should be too low, got %q", sender.last())
	}

	if err := h.HandleGuess(nil, newGuessMsg("/guess 90")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sender.last(), "много") {
		t.Errorf("90 should be too high, got %q", sender.last())
	}

	winMsg := newGuessMsg("/guess 50")
	winMsg.From = &telego.User{ID: 300, Username: "bob", FirstName: "Bob"}
	if err := h.HandleGuess(nil, winMsg); err != nil {
		t.Fatal(err)
	}
	body := sender.last()
	if !strings.Contains(body, "угадал") || !strings.Contains(body, "bob") {
		t.Errorf("50 should win for bob, got %q", body)
	}
	if !strings.Contains(body, "50") {
		t.Errorf("win message should reveal the secret 50, got %q", body)
	}
	if _, err := store.GetRound(context.Background(), 1001234567890); err != guess.ErrNotFound {
		t.Errorf("round should be deleted after the win, got %v", err)
	}
}

func TestGuessHandlerNonNumericArg(t *testing.T) {
	h, sender, _ := newGuessTestHandler(0)
	if err := h.HandleGuess(nil, newGuessMsg("/guess")); err != nil {
		t.Fatal(err)
	}
	if err := h.HandleGuess(nil, newGuessMsg("/guess abc")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sender.last(), "не число") {
		t.Errorf("non-numeric arg should get a hint, got %q", sender.last())
	}
}

func TestGuessHandlerOutOfRangeArg(t *testing.T) {
	h, sender, _ := newGuessTestHandler(0)
	if err := h.HandleGuess(nil, newGuessMsg("/guess")); err != nil {
		t.Fatal(err)
	}
	if err := h.HandleGuess(nil, newGuessMsg("/guess 0")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sender.last(), "вне диапазона") {
		t.Errorf("0 should be out of range, got %q", sender.last())
	}
	if err := h.HandleGuess(nil, newGuessMsg("/guess 101")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sender.last(), "вне диапазона") {
		t.Errorf("101 should be out of range, got %q", sender.last())
	}
}

func TestGuessHandlerGuessWithNoRound(t *testing.T) {
	h, sender, _ := newGuessTestHandler(0)
	if err := h.HandleGuess(nil, newGuessMsg("/guess 50")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sender.last(), "нет активного раунда") {
		t.Errorf("guess with no round should hint to start, got %q", sender.last())
	}
}

func TestGuessHandlerTopEmptyAndPopulated(t *testing.T) {
	h, sender, _ := newGuessTestHandler(0) // secret 1
	if err := h.HandleGuess(nil, newGuessMsg("/guess top")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sender.last(), "никто не угадал") {
		t.Errorf("empty leaderboard message expected, got %q", sender.last())
	}
	// Win once, then top must list the winner.
	if err := h.HandleGuess(nil, newGuessMsg("/guess")); err != nil {
		t.Fatal(err)
	}
	if err := h.HandleGuess(nil, newGuessMsg("/guess 1")); err != nil {
		t.Fatal(err)
	}
	if err := h.HandleGuess(nil, newGuessMsg("/guess top")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sender.last(), "alice") {
		t.Errorf("leaderboard should list @alice, got %q", sender.last())
	}
}

func TestGuessHandlerIgnoresBotAndNilFrom(t *testing.T) {
	h, sender, _ := newGuessTestHandler(0)
	m := newGuessMsg("/guess")
	m.From = nil
	if err := h.HandleGuess(nil, m); err != nil {
		t.Fatal(err)
	}
	m2 := newGuessMsg("/guess")
	m2.From = &telego.User{ID: 5, IsBot: true}
	if err := h.HandleGuess(nil, m2); err != nil {
		t.Fatal(err)
	}
	if sender.count() != 0 {
		t.Errorf("bot/nil sender messages must be ignored, got %d replies", sender.count())
	}
}
