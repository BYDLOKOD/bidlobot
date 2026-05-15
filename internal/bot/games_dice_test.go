package bot

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/games/dice"
)

// stubDiceSender records the parameters passed by the handler so tests
// can assert what would have been sent to Telegram. The Dice value the
// stub returns is fixed in NextDice; SendDice errors when SendDiceErr is
// non-nil.
type stubDiceSender struct {
	mu sync.Mutex

	NextDice    int
	SendDiceErr error

	DiceCalls    []*telego.SendDiceParams
	MessageCalls []*telego.SendMessageParams
}

func newStubDiceSender(nextDice int) *stubDiceSender {
	return &stubDiceSender{NextDice: nextDice}
}

func (s *stubDiceSender) SendDice(_ context.Context, params *telego.SendDiceParams) (*telego.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.DiceCalls = append(s.DiceCalls, params)
	if s.SendDiceErr != nil {
		return nil, s.SendDiceErr
	}
	emoji := params.Emoji
	if emoji == "" {
		emoji = dice.DefaultEmoji
	}
	return &telego.Message{
		MessageID: 999,
		Date:      int64(time.Now().Unix()),
		Dice:      &telego.Dice{Emoji: emoji, Value: s.NextDice},
	}, nil
}

func (s *stubDiceSender) SendMessage(_ context.Context, params *telego.SendMessageParams) (*telego.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.MessageCalls = append(s.MessageCalls, params)
	return &telego.Message{MessageID: 1000}, nil
}

// memDiceStore is a tiny in-memory dice.Store for handler tests so we
// do not need a real bbolt instance for unit-level coverage.
type memDiceStore struct {
	mu   sync.Mutex
	data map[string]dice.Record
}

func newMemDiceStore() *memDiceStore { return &memDiceStore{data: make(map[string]dice.Record)} }

func diceMemKey(chatID int64, emoji string) string {
	return string(rune(chatID&0xffff)) + "/" + emoji
}

func (m *memDiceStore) Get(_ context.Context, absChatID int64, emoji string) (*dice.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.data[diceMemKey(absChatID, emoji)]
	if !ok {
		return nil, dice.ErrNotFound
	}
	cp := r
	return &cp, nil
}

func (m *memDiceStore) Put(_ context.Context, r dice.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[diceMemKey(r.AbsChatID, r.Emoji)] = r
	return nil
}

func newDiceTestHandler(nextDice int) (*DiceHandler, *stubDiceSender, *memDiceStore) {
	store := newMemDiceStore()
	svc := dice.NewService(store, testLogger())
	bot := newStubDiceSender(nextDice)
	return NewDiceHandler(svc, bot, testLogger()), bot, store
}

func newDiceTestMessage(text string) telego.Message {
	return telego.Message{
		MessageID: 1,
		Date:      int64(time.Now().Unix()),
		Chat:      telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
		Text:      text,
	}
}

func TestDiceHandlerSendsDefaultEmoji(t *testing.T) {
	h, bot, _ := newDiceTestHandler(4)
	msg := newDiceTestMessage("/dice")
	if err := h.HandleDice(nil, msg); err != nil {
		t.Fatalf("HandleDice: %v", err)
	}
	if len(bot.DiceCalls) != 1 {
		t.Fatalf("expected 1 SendDice call, got %d", len(bot.DiceCalls))
	}
	if bot.DiceCalls[0].Emoji != dice.DefaultEmoji {
		t.Errorf("expected default emoji, got %q", bot.DiceCalls[0].Emoji)
	}
}

func TestDiceHandlerSendsCustomEmoji(t *testing.T) {
	h, bot, _ := newDiceTestHandler(3)
	msg := newDiceTestMessage("/dice \U0001F3AF")
	if err := h.HandleDice(nil, msg); err != nil {
		t.Fatal(err)
	}
	if bot.DiceCalls[0].Emoji != "\U0001F3AF" {
		t.Errorf("expected dart emoji, got %q", bot.DiceCalls[0].Emoji)
	}
}

func TestDiceHandlerRejectsUnsupportedEmoji(t *testing.T) {
	h, bot, _ := newDiceTestHandler(6)
	msg := newDiceTestMessage("/dice abc")
	if err := h.HandleDice(nil, msg); err != nil {
		t.Fatal(err)
	}
	if len(bot.DiceCalls) != 0 {
		t.Fatal("must not call SendDice for unsupported emoji")
	}
	if len(bot.MessageCalls) != 1 {
		t.Fatalf("expected hint reply, got %d messages", len(bot.MessageCalls))
	}
	if !strings.Contains(bot.MessageCalls[0].Text, "Поддерживаются") {
		t.Errorf("expected hint text, got %q", bot.MessageCalls[0].Text)
	}
}

func TestDiceHandlerFirstRollAnnouncesRecord(t *testing.T) {
	h, bot, _ := newDiceTestHandler(6)
	msg := newDiceTestMessage("/dice")
	if err := h.HandleDice(nil, msg); err != nil {
		t.Fatal(err)
	}
	if len(bot.MessageCalls) != 1 {
		t.Fatalf("expected announcement, got %d messages", len(bot.MessageCalls))
	}
	body := bot.MessageCalls[0].Text
	if !strings.Contains(body, "Первый рекорд") {
		t.Errorf("first roll should announce first record, got %q", body)
	}
	if !strings.Contains(body, "alice") {
		t.Errorf("announcement should include user, got %q", body)
	}
}

func TestDiceHandlerLowerRollSilent(t *testing.T) {
	h, bot, store := newDiceTestHandler(6)
	// First roll sets the record at 6.
	if err := h.HandleDice(nil, newDiceTestMessage("/dice")); err != nil {
		t.Fatal(err)
	}
	// Reset announcements so we can assert silence on the second.
	bot.mu.Lock()
	bot.MessageCalls = nil
	bot.mu.Unlock()
	bot.NextDice = 3

	if err := h.HandleDice(nil, newDiceTestMessage("/dice")); err != nil {
		t.Fatal(err)
	}
	if len(bot.MessageCalls) != 0 {
		t.Errorf("lower roll must not announce; got %d messages: %+v", len(bot.MessageCalls), bot.MessageCalls)
	}
	got, _ := store.Get(context.Background(), 1001234567890, dice.DefaultEmoji)
	if got.Value != 6 {
		t.Errorf("record should remain at 6, got %d", got.Value)
	}
}

func TestDiceHandlerNewRecordAnnouncesPrevious(t *testing.T) {
	h, bot, _ := newDiceTestHandler(4)
	if err := h.HandleDice(nil, newDiceTestMessage("/dice")); err != nil {
		t.Fatal(err)
	}
	bot.mu.Lock()
	bot.MessageCalls = nil
	bot.NextDice = 6
	bot.mu.Unlock()

	msg2 := newDiceTestMessage("/dice")
	msg2.From = &telego.User{ID: 300, Username: "bob", FirstName: "Bob"}
	if err := h.HandleDice(nil, msg2); err != nil {
		t.Fatal(err)
	}
	if len(bot.MessageCalls) != 1 {
		t.Fatalf("expected new-record announcement, got %d", len(bot.MessageCalls))
	}
	body := bot.MessageCalls[0].Text
	if !strings.Contains(body, "Новый рекорд") {
		t.Errorf("expected new-record text, got %q", body)
	}
	if !strings.Contains(body, "bob") {
		t.Errorf("expected @bob in announcement, got %q", body)
	}
	if !strings.Contains(body, "alice") {
		t.Errorf("expected previous holder @alice, got %q", body)
	}
}

func TestDiceHandlerTieAnnouncesRepeat(t *testing.T) {
	h, bot, _ := newDiceTestHandler(6)
	if err := h.HandleDice(nil, newDiceTestMessage("/dice")); err != nil {
		t.Fatal(err)
	}
	bot.mu.Lock()
	bot.MessageCalls = nil
	bot.mu.Unlock()

	msg2 := newDiceTestMessage("/dice")
	msg2.From = &telego.User{ID: 300, Username: "bob", FirstName: "Bob"}
	if err := h.HandleDice(nil, msg2); err != nil {
		t.Fatal(err)
	}
	if len(bot.MessageCalls) != 1 {
		t.Fatalf("expected tie announcement, got %d", len(bot.MessageCalls))
	}
	body := bot.MessageCalls[0].Text
	if !strings.Contains(body, "Повтор рекорда") {
		t.Errorf("expected tie text, got %q", body)
	}
	if !strings.Contains(body, "alice") {
		t.Errorf("tie should still credit original holder, got %q", body)
	}
}

func TestDiceHandlerSendDiceErrorReplies(t *testing.T) {
	h, bot, _ := newDiceTestHandler(6)
	bot.SendDiceErr = errors.New("rate limit")

	if err := h.HandleDice(nil, newDiceTestMessage("/dice")); err != nil {
		t.Fatal(err)
	}
	if len(bot.MessageCalls) != 1 {
		t.Fatalf("expected fallback reply, got %d", len(bot.MessageCalls))
	}
	if !strings.Contains(bot.MessageCalls[0].Text, "Не удалось") {
		t.Errorf("expected error reply, got %q", bot.MessageCalls[0].Text)
	}
}

func TestDiceHandlerNoFromIgnored(t *testing.T) {
	h, bot, _ := newDiceTestHandler(6)
	msg := newDiceTestMessage("/dice")
	msg.From = nil
	if err := h.HandleDice(nil, msg); err != nil {
		t.Fatal(err)
	}
	if len(bot.DiceCalls) != 0 || len(bot.MessageCalls) != 0 {
		t.Errorf("anonymous message must be ignored entirely; dice=%d msg=%d", len(bot.DiceCalls), len(bot.MessageCalls))
	}
}

func TestDiceHandlerEmojiHintLists(t *testing.T) {
	hint := diceHintMessage()
	for _, e := range dice.AllowedEmojis {
		if !strings.Contains(hint, e) {
			t.Errorf("hint missing emoji %s: %q", e, hint)
		}
	}
}
