package bot

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/games/dice"
)

// stubDuelSender returns successive dice values from Rolls (one per
// SendDice call) and records every SendMessage.
type stubDuelSender struct {
	mu sync.Mutex

	Rolls       []int // consumed in order, one per SendDice call
	rollIdx     int
	SendDiceErr error

	DiceCalls    []*telego.SendDiceParams
	MessageCalls []*telego.SendMessageParams
}

func (s *stubDuelSender) SendDice(_ context.Context, p *telego.SendDiceParams) (*telego.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.DiceCalls = append(s.DiceCalls, p)
	if s.SendDiceErr != nil {
		return nil, s.SendDiceErr
	}
	v := 1
	if s.rollIdx < len(s.Rolls) {
		v = s.Rolls[s.rollIdx]
	}
	s.rollIdx++
	return &telego.Message{
		MessageID: 900 + s.rollIdx,
		Date:      int64(time.Now().Unix()),
		Dice:      &telego.Dice{Emoji: p.Emoji, Value: v},
	}, nil
}

func (s *stubDuelSender) SendMessage(_ context.Context, p *telego.SendMessageParams) (*telego.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.MessageCalls = append(s.MessageCalls, p)
	return &telego.Message{MessageID: 1000}, nil
}

func (s *stubDuelSender) lastMsg() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.MessageCalls) == 0 {
		return ""
	}
	return s.MessageCalls[len(s.MessageCalls)-1].Text
}

func (s *stubDuelSender) diceCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.DiceCalls)
}

func (s *stubDuelSender) msgCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.MessageCalls)
}

// stubDuelMembers resolves a username to a member only if it is in the
// known set - mirroring the real repo, which errors for any user the
// bot has never observed in the chat.
type stubDuelMembers struct {
	byName map[string]*membership.Member
}

func (s *stubDuelMembers) GetMemberByUsername(_ context.Context, _ int64, username string) (*membership.Member, error) {
	if m, ok := s.byName[strings.ToLower(username)]; ok {
		return m, nil
	}
	return nil, membership.ErrNotFound
}

// knownBob is the default opponent the proceed-path tests duel against;
// id differs from the caller (200) and it is not a bot.
func knownBob() *stubDuelMembers {
	return &stubDuelMembers{byName: map[string]*membership.Member{
		"bob": {UserID: 300, Username: "bob", FirstName: "Bob"},
	}}
}

func newDuelMsg(text string) telego.Message {
	return telego.Message{
		MessageID: 1,
		Date:      int64(time.Now().Unix()),
		Chat:      telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
		Text:      text,
	}
}

func TestDuelHandlerChallengerWins(t *testing.T) {
	sender := &stubDuelSender{Rolls: []int{6, 2}} // challenger 6, opponent 2
	h := NewDuelHandler(sender, knownBob(), "bidlobot", testLogger())
	if err := h.HandleDuel(nil, newDuelMsg("/duel @bob")); err != nil {
		t.Fatal(err)
	}
	if sender.diceCount() != 2 {
		t.Fatalf("expected 2 SendDice calls, got %d", sender.diceCount())
	}
	body := sender.lastMsg()
	// Inert identity: both duelists named, but NEVER as "@handle"
	// (that would notify them - the whole point of the fix).
	if strings.Contains(body, "@") {
		t.Errorf("announcement must not contain a literal @-mention, got %q", body)
	}
	if !strings.Contains(body, "alice") || !strings.Contains(body, "bob") {
		t.Errorf("announcement should name both duelists, got %q", body)
	}
	if !strings.Contains(body, "Побеждает alice") {
		t.Errorf("challenger (6) should win over opponent (2), got %q", body)
	}
}

func TestDuelHandlerOpponentWins(t *testing.T) {
	sender := &stubDuelSender{Rolls: []int{1, 5}}
	h := NewDuelHandler(sender, knownBob(), "bidlobot", testLogger())
	if err := h.HandleDuel(nil, newDuelMsg("/duel @bob")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sender.lastMsg(), "Побеждает bob") {
		t.Errorf("opponent (5) should win over challenger (1), got %q", sender.lastMsg())
	}
}

func TestDuelHandlerTie(t *testing.T) {
	sender := &stubDuelSender{Rolls: []int{4, 4}}
	h := NewDuelHandler(sender, knownBob(), "bidlobot", testLogger())
	if err := h.HandleDuel(nil, newDuelMsg("/duel @bob")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sender.lastMsg(), "Ничья") {
		t.Errorf("equal rolls should be a tie, got %q", sender.lastMsg())
	}
}

func TestDuelHandlerNoTarget(t *testing.T) {
	sender := &stubDuelSender{Rolls: []int{6, 1}}
	h := NewDuelHandler(sender, knownBob(), "bidlobot", testLogger())
	if err := h.HandleDuel(nil, newDuelMsg("/duel")); err != nil {
		t.Fatal(err)
	}
	if sender.diceCount() != 0 {
		t.Error("no dice should be rolled without a target")
	}
	if !strings.Contains(sender.lastMsg(), "Кого вызываем") {
		t.Errorf("missing target should get a hint, got %q", sender.lastMsg())
	}
}

func TestDuelHandlerSelfTarget(t *testing.T) {
	sender := &stubDuelSender{Rolls: []int{6, 1}}
	h := NewDuelHandler(sender, knownBob(), "bidlobot", testLogger())
	if err := h.HandleDuel(nil, newDuelMsg("/duel @alice")); err != nil {
		t.Fatal(err)
	}
	if sender.diceCount() != 0 {
		t.Error("self-duel must not roll dice")
	}
	if !strings.Contains(sender.lastMsg(), "самим собой") {
		t.Errorf("self-duel should be rejected, got %q", sender.lastMsg())
	}
}

func TestDuelHandlerBotTarget(t *testing.T) {
	sender := &stubDuelSender{Rolls: []int{6, 1}}
	h := NewDuelHandler(sender, knownBob(), "bidlobot", testLogger())
	if err := h.HandleDuel(nil, newDuelMsg("/duel @BidloBot")); err != nil {
		t.Fatal(err)
	}
	if sender.diceCount() != 0 {
		t.Error("dueling the bot must not roll dice")
	}
	if !strings.Contains(sender.lastMsg(), "Я не дуэлюсь") {
		t.Errorf("bot-duel should be rejected, got %q", sender.lastMsg())
	}
}

// TestDuelHandlerRejectsNonMember is the regression guard for the
// owner-reported bug: "/duel @anyone" could challenge a person the bot
// never saw in the chat (even a garbage handle). The opponent must
// resolve to a known member or the duel is refused with no dice and no
// @-mention echoed back.
func TestDuelHandlerRejectsNonMember(t *testing.T) {
	sender := &stubDuelSender{Rolls: []int{6, 1}}
	h := NewDuelHandler(sender, knownBob(), "bidlobot", testLogger())
	if err := h.HandleDuel(nil, newDuelMsg("/duel @stranger")); err != nil {
		t.Fatal(err)
	}
	if sender.diceCount() != 0 {
		t.Error("a non-member target must not roll dice")
	}
	body := sender.lastMsg()
	if !strings.Contains(body, "Не знаю участника") {
		t.Errorf("non-member should be rejected with a hint, got %q", body)
	}
	if strings.Contains(body, "@") {
		t.Errorf("rejection must stay inert (a lurking member could be pinged), got %q", body)
	}
}

func TestDuelHandlerSendDiceErrorAborts(t *testing.T) {
	sender := &stubDuelSender{Rolls: []int{6, 2}, SendDiceErr: errors.New("rate limit")}
	h := NewDuelHandler(sender, knownBob(), "bidlobot", testLogger())
	if err := h.HandleDuel(nil, newDuelMsg("/duel @bob")); err != nil {
		t.Fatal(err)
	}
	// First SendDice fails -> exactly one failure notice, no result line.
	if sender.msgCount() != 1 {
		t.Fatalf("expected exactly 1 failure notice, got %d", sender.msgCount())
	}
	if !strings.Contains(sender.lastMsg(), "Не удалось бросить") {
		t.Errorf("expected dice-failure notice, got %q", sender.lastMsg())
	}
}

func TestDuelHandlerIgnoresBotAndNilFrom(t *testing.T) {
	sender := &stubDuelSender{Rolls: []int{6, 1}}
	h := NewDuelHandler(sender, knownBob(), "bidlobot", testLogger())
	m := newDuelMsg("/duel @bob")
	m.From = nil
	if err := h.HandleDuel(nil, m); err != nil {
		t.Fatal(err)
	}
	m2 := newDuelMsg("/duel @bob")
	m2.From = &telego.User{ID: 9, IsBot: true}
	if err := h.HandleDuel(nil, m2); err != nil {
		t.Fatal(err)
	}
	if sender.diceCount() != 0 || sender.msgCount() != 0 {
		t.Errorf("bot/nil sender must be ignored, dice=%d msg=%d", sender.diceCount(), sender.msgCount())
	}
}

// Guard: the duel handler relies on the standard 🎲 emoji being a valid
// dice emoji so SendDice yields 1..6.
func TestDuelUsesStandardDie(t *testing.T) {
	if !dice.IsAllowedEmoji(dice.DefaultEmoji) {
		t.Fatal("duel depends on DefaultEmoji being a valid dice emoji")
	}
}
