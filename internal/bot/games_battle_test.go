package bot

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/games/battle"
)

// stubBattleSender records calls and assigns sequential message IDs so
// tests can assert what was posted and exercise the registry lookup
// flow.
type stubBattleSender struct {
	mu sync.Mutex

	NextMessageIDs []int
	SendErr        error

	Sent []*telego.SendMessageParams
}

func (s *stubBattleSender) SendMessage(_ context.Context, params *telego.SendMessageParams) (*telego.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Sent = append(s.Sent, params)
	if s.SendErr != nil {
		return nil, s.SendErr
	}
	id := 100 + len(s.Sent)
	if len(s.NextMessageIDs) > 0 {
		id = s.NextMessageIDs[0]
		s.NextMessageIDs = s.NextMessageIDs[1:]
	}
	return &telego.Message{MessageID: id}, nil
}

// fakeBattleClock is a controllable clock: After returns a channel the
// test fires manually via Trigger().
type fakeBattleClock struct {
	now time.Time

	mu      sync.Mutex
	pending []chan time.Time
}

func newFakeBattleClock(t time.Time) *fakeBattleClock {
	return &fakeBattleClock{now: t}
}

func (c *fakeBattleClock) Now() time.Time { return c.now }

func (c *fakeBattleClock) After(_ time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.pending = append(c.pending, ch)
	return ch
}

func (c *fakeBattleClock) Advance(d time.Duration) {
	c.now = c.now.Add(d)
}

// FireAll signals every outstanding After channel, simulating timer
// expiry for every battle in flight.
func (c *fakeBattleClock) FireAll() {
	c.mu.Lock()
	pending := c.pending
	c.pending = nil
	c.mu.Unlock()
	for _, ch := range pending {
		ch <- c.now
	}
}

func newBattleHandlerForTest() (*BattleHandler, *stubBattleSender, *fakeBattleClock, *battle.Registry, *idgen) {
	registry := battle.NewRegistry()
	bot := &stubBattleSender{NextMessageIDs: []int{200, 201, 202}}
	clock := newFakeBattleClock(time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC))
	gen := &idgen{}
	h := &BattleHandler{
		registry: registry,
		bot:      bot,
		clock:    clock,
		log:      testLogger(),
		duration: 5 * time.Second,
		nextID:   gen.next,
	}
	return h, bot, clock, registry, gen
}

type idgen struct {
	count atomic.Int64
}

func (g *idgen) next() (string, error) {
	n := g.count.Add(1)
	return "battle-" + string(rune('0'+n)), nil
}

func newBattleMessage(text string) telego.Message {
	return telego.Message{
		MessageID: 1,
		Date:      int64(time.Now().Unix()),
		Chat:      telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
		Text:      text,
	}
}

func TestBattleHandlerHintOnMissingArgs(t *testing.T) {
	h, bot, _, registry, _ := newBattleHandlerForTest()
	for _, text := range []string{"/battle", "/battle solo"} {
		bot.Sent = nil
		if err := h.HandleBattle(nil, newBattleMessage(text)); err != nil {
			t.Fatalf("%q: %v", text, err)
		}
		if len(bot.Sent) != 1 {
			t.Errorf("%q: expected hint reply, got %d messages", text, len(bot.Sent))
		}
		if registry.Active() != 0 {
			t.Errorf("%q: must not register a battle on hint", text)
		}
	}
}

func TestBattleHandlerSetsUpThreeMessages(t *testing.T) {
	h, bot, _, registry, _ := newBattleHandlerForTest()
	if err := h.HandleBattle(nil, newBattleMessage("/battle Go Rust")); err != nil {
		t.Fatal(err)
	}
	if len(bot.Sent) != 3 {
		t.Fatalf("expected header + two side messages, got %d", len(bot.Sent))
	}
	if registry.Active() != 1 {
		t.Fatalf("expected 1 active battle, got %d", registry.Active())
	}
	// Header should mention both sides
	if !strings.Contains(bot.Sent[0].Text, "Go") || !strings.Contains(bot.Sent[0].Text, "Rust") {
		t.Errorf("header missing labels: %q", bot.Sent[0].Text)
	}
	// Side messages refer to their respective label
	if !strings.Contains(bot.Sent[1].Text, "Go") {
		t.Errorf("left side message missing Go: %q", bot.Sent[1].Text)
	}
	if !strings.Contains(bot.Sent[2].Text, "Rust") {
		t.Errorf("right side message missing Rust: %q", bot.Sent[2].Text)
	}
}

func TestBattleHandlerRegistersMessageIDs(t *testing.T) {
	h, _, _, registry, _ := newBattleHandlerForTest()
	if err := h.HandleBattle(nil, newBattleMessage("/battle Go Rust")); err != nil {
		t.Fatal(err)
	}
	// Stub assigned 200 (header), 201 (left side), 202 (right side)
	left, side, ok := registry.LookupByMessageID(201)
	if !ok || side != battle.SideLeft {
		t.Errorf("left lookup failed: %v %v", side, ok)
	}
	right, side, ok := registry.LookupByMessageID(202)
	if !ok || side != battle.SideRight {
		t.Errorf("right lookup failed: %v %v", side, ok)
	}
	if left != right {
		t.Error("left and right lookups should resolve to the same battle")
	}
}

func TestBattleObserveReactionRecordsVote(t *testing.T) {
	h, _, _, registry, _ := newBattleHandlerForTest()
	if err := h.HandleBattle(nil, newBattleMessage("/battle Go Rust")); err != nil {
		t.Fatal(err)
	}

	// User reacts to left side
	if err := h.ObserveReaction(nil, telego.MessageReactionUpdated{
		Chat:        telego.Chat{ID: -1001234567890},
		MessageID:   201,
		User:        &telego.User{ID: 300, Username: "bob"},
		NewReaction: []telego.ReactionType{&telego.ReactionTypeEmoji{Emoji: "👍"}},
		Date:        time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}

	for _, b := range allBattles(registry) {
		r := b.Tally(time.Now())
		if r.LeftVotes != 1 || r.RightVotes != 0 {
			t.Errorf("expected 1-0, got %+v", r)
		}
	}
}

func TestBattleObserveReactionIgnoresBots(t *testing.T) {
	h, _, _, registry, _ := newBattleHandlerForTest()
	_ = h.HandleBattle(nil, newBattleMessage("/battle Go Rust"))

	if err := h.ObserveReaction(nil, telego.MessageReactionUpdated{
		Chat:        telego.Chat{ID: -1001234567890},
		MessageID:   201,
		User:        &telego.User{ID: 300, IsBot: true},
		NewReaction: []telego.ReactionType{&telego.ReactionTypeEmoji{Emoji: "👍"}},
		Date:        time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	for _, b := range allBattles(registry) {
		r := b.Tally(time.Now())
		if r.LeftVotes != 0 {
			t.Errorf("bot reaction must not count, got %+v", r)
		}
	}
}

func TestBattleObserveReactionIgnoresUnknownMessage(t *testing.T) {
	h, _, _, _, _ := newBattleHandlerForTest()
	// no battle registered
	err := h.ObserveReaction(nil, telego.MessageReactionUpdated{
		Chat:        telego.Chat{ID: -1001234567890},
		MessageID:   999,
		User:        &telego.User{ID: 300},
		NewReaction: []telego.ReactionType{&telego.ReactionTypeEmoji{Emoji: "👍"}},
		Date:        time.Now().Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBattleObserveReactionIgnoresRemoval(t *testing.T) {
	h, _, _, registry, _ := newBattleHandlerForTest()
	_ = h.HandleBattle(nil, newBattleMessage("/battle Go Rust"))

	// Reaction removed (NewReaction empty)
	err := h.ObserveReaction(nil, telego.MessageReactionUpdated{
		Chat:        telego.Chat{ID: -1001234567890},
		MessageID:   201,
		User:        &telego.User{ID: 300},
		NewReaction: nil,
		Date:        time.Now().Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range allBattles(registry) {
		r := b.Tally(time.Now())
		if r.LeftVotes != 0 || r.RightVotes != 0 {
			t.Errorf("removal must not count: %+v", r)
		}
	}
}

func TestBattleCloseTimerAnnouncesResult(t *testing.T) {
	h, bot, clock, registry, _ := newBattleHandlerForTest()
	if err := h.HandleBattle(nil, newBattleMessage("/battle Go Rust")); err != nil {
		t.Fatal(err)
	}

	// Vote 2 left, 1 right
	for _, uid := range []int64{300, 301} {
		_ = h.ObserveReaction(nil, telego.MessageReactionUpdated{
			Chat:        telego.Chat{ID: -1001234567890},
			MessageID:   201,
			User:        &telego.User{ID: uid},
			NewReaction: []telego.ReactionType{&telego.ReactionTypeEmoji{Emoji: "👍"}},
			Date:        clock.Now().Unix(),
		})
	}
	_ = h.ObserveReaction(nil, telego.MessageReactionUpdated{
		Chat:        telego.Chat{ID: -1001234567890},
		MessageID:   202,
		User:        &telego.User{ID: 302},
		NewReaction: []telego.ReactionType{&telego.ReactionTypeEmoji{Emoji: "👍"}},
		Date:        clock.Now().Unix(),
	})

	// Capture initial sent count, then advance & fire timer
	sentBefore := len(bot.Sent)
	clock.Advance(6 * time.Second)
	clock.FireAll()

	// Wait for goroutine to post result
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		bot.mu.Lock()
		got := len(bot.Sent)
		bot.mu.Unlock()
		if got > sentBefore {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	bot.mu.Lock()
	final := bot.Sent[len(bot.Sent)-1]
	bot.mu.Unlock()
	if !strings.Contains(final.Text, "Побеждает <b>Go</b>") {
		t.Errorf("expected Go winner announcement, got %q", final.Text)
	}
	if registry.Active() != 0 {
		t.Errorf("registry must be empty after close, got %d", registry.Active())
	}
}

func TestBattleCloseTimerNoVotesAnnouncesTie(t *testing.T) {
	h, bot, clock, _, _ := newBattleHandlerForTest()
	if err := h.HandleBattle(nil, newBattleMessage("/battle Go Rust")); err != nil {
		t.Fatal(err)
	}
	sentBefore := len(bot.Sent)
	clock.Advance(6 * time.Second)
	clock.FireAll()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		bot.mu.Lock()
		got := len(bot.Sent)
		bot.mu.Unlock()
		if got > sentBefore {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	bot.mu.Lock()
	final := bot.Sent[len(bot.Sent)-1]
	bot.mu.Unlock()
	if !strings.Contains(final.Text, "Никто не проголосовал") {
		t.Errorf("expected no-votes message, got %q", final.Text)
	}
}

func TestBattleObserveReactionLateIgnored(t *testing.T) {
	h, _, clock, registry, _ := newBattleHandlerForTest()
	_ = h.HandleBattle(nil, newBattleMessage("/battle Go Rust"))

	// Move clock past EndsAt without firing the timer (simulates a
	// reaction delivered after the window but before the goroutine
	// removed the battle).
	clock.Advance(10 * time.Second)
	err := h.ObserveReaction(nil, telego.MessageReactionUpdated{
		Chat:        telego.Chat{ID: -1001234567890},
		MessageID:   201,
		User:        &telego.User{ID: 300},
		NewReaction: []telego.ReactionType{&telego.ReactionTypeEmoji{Emoji: "👍"}},
		Date:        clock.Now().Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range allBattles(registry) {
		r := b.Tally(clock.Now())
		if r.LeftVotes != 0 {
			t.Errorf("late reaction must be ignored, got %+v", r)
		}
	}
}

func TestBattleHandlerSendErrorRollsBack(t *testing.T) {
	h, bot, _, registry, _ := newBattleHandlerForTest()
	bot.SendErr = errors.New("rate limit")
	if err := h.HandleBattle(nil, newBattleMessage("/battle Go Rust")); err != nil {
		t.Fatal(err)
	}
	if registry.Active() != 0 {
		t.Errorf("send error must roll back the registry, got %d active", registry.Active())
	}
}

func TestBattleHandlerOverlongLabelsRejected(t *testing.T) {
	h, bot, _, registry, _ := newBattleHandlerForTest()
	long := strings.Repeat("x", battle.MaxLabelLen+5)
	if err := h.HandleBattle(nil, newBattleMessage("/battle "+long+" Y")); err != nil {
		t.Fatal(err)
	}
	if len(bot.Sent) != 1 {
		t.Errorf("expected single hint reply, got %d", len(bot.Sent))
	}
	if registry.Active() != 0 {
		t.Errorf("must not register over-long battle, got %d", registry.Active())
	}
}

func TestParseBattleArgsJoinsTrailingTokens(t *testing.T) {
	left, right, ok := parseBattleArgs("/battle Go Rust language")
	if !ok || left != "Go" || right != "Rust language" {
		t.Errorf("got (%q, %q, %v), want (Go, Rust language, true)", left, right, ok)
	}
}

// allBattles returns every battle registered. Used by tests that need
// to iterate the (single) entry without knowing the generated ID.
func allBattles(r *battle.Registry) []*battle.Battle {
	// The registry's internal byID map isn't exported, but we can scan
	// likely IDs produced by the test idgen. The idgen uses
	// "battle-1", "battle-2", ...; in tests we only ever generate one
	// or two so a small probe is enough.
	out := make([]*battle.Battle, 0, 2)
	for i := 1; i <= 5; i++ {
		id := "battle-" + string(rune('0'+i))
		if b := r.Get(id); b != nil {
			out = append(out, b)
		}
	}
	return out
}
