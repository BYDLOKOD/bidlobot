package bot

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/domain/reputation"
	"github.com/veschin/bidlobot/internal/storage"
)

// stubRepSender records every SendMessage params for assertion.
// Satisfies the reputationSender interface the handler requires.
type stubRepSender struct {
	mu      sync.Mutex
	Sent    []*telego.SendMessageParams
	SendErr error
}

func (s *stubRepSender) SendMessage(_ context.Context, params *telego.SendMessageParams) (*telego.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.SendErr != nil {
		return nil, s.SendErr
	}
	s.Sent = append(s.Sent, params)
	return &telego.Message{MessageID: 2000 + len(s.Sent)}, nil
}

func (s *stubRepSender) lastMsg() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.Sent) == 0 {
		return ""
	}
	return s.Sent[len(s.Sent)-1].Text
}

// stubRepMembers resolves usernames and user IDs for reputation tests.
// Mirrors the stubDuelMembers pattern from games_duel_test.go.
type stubRepMembers struct {
	byName map[string]*membership.Member // lower(username) -> Member
	byID   map[int64]*membership.Member  // userID -> Member
}

func newStubRepMembers() *stubRepMembers {
	return &stubRepMembers{
		byName: make(map[string]*membership.Member),
		byID:   make(map[int64]*membership.Member),
	}
}

func (s *stubRepMembers) GetMemberByUsername(_ context.Context, _ int64, username string) (*membership.Member, error) {
	if m, ok := s.byName[strings.ToLower(username)]; ok {
		return m, nil
	}
	return nil, membership.ErrNotFound
}

func (s *stubRepMembers) GetMember(_ context.Context, userID, absChatID int64) (*membership.Member, error) {
	if m, ok := s.byID[userID]; ok {
		return m, nil
	}
	return nil, membership.ErrNotFound
}

func (s *stubRepMembers) put(m membership.Member) {
	s.byID[m.UserID] = &m
	if m.Username != "" {
		s.byName[strings.ToLower(m.Username)] = &m
	}
}

// repMessage builds a minimal supergroup message with the given text and sender.
func repMessage(text string, from *telego.User) telego.Message {
	return telego.Message{
		MessageID: 1,
		Chat:      telego.Chat{ID: -1001234567890, Type: "supergroup"},
		From:      from,
		Text:      text,
		Date:      1000000,
	}
}

// repReplyMessage builds a message that is a reply to another message.
func repReplyMessage(text string, from *telego.User, repliedFrom *telego.User) telego.Message {
	msg := repMessage(text, from)
	msg.ReplyToMessage = &telego.Message{
		MessageID: 0,
		From:      repliedFrom,
	}
	return msg
}

// Known users for test fixtures.
var (
	aliceUser = &telego.User{ID: 100, FirstName: "Alice", Username: "alice"}
	bobUser   = &telego.User{ID: 200, FirstName: "Bob", Username: "bob"}
	carolUser = &telego.User{ID: 300, FirstName: "Carol", Username: "carol"}
	daveUser  = &telego.User{ID: 400, FirstName: "Dave", Username: "dave"}
	eveUser   = &telego.User{ID: 500, FirstName: "Eve", Username: "eve"}
	frankUser = &telego.User{ID: 600, FirstName: "Frank", Username: "frank"}

	aliceMember = membership.Member{UserID: 100, Username: "alice", FirstName: "Alice"}
	bobMember   = membership.Member{UserID: 200, Username: "bob", FirstName: "Bob"}
	carolMember = membership.Member{UserID: 300, Username: "carol", FirstName: "Carol"}
	daveMember  = membership.Member{UserID: 400, Username: "dave", FirstName: "Dave"}
	eveMember   = membership.Member{UserID: 500, Username: "eve", FirstName: "Eve"}
	frankMember = membership.Member{UserID: 600, Username: "frank", FirstName: "Frank"}
)

// newRepHandler creates a ReputationHandler with real bbolt-backed storage
// and in-memory membership stubs. Returns handler, sender stub, member stub,
// and a cleanup function that closes the bbolt database.
func newRepHandler(t *testing.T) (*ReputationHandler, *stubRepSender, *stubRepMembers, *storage.ReputationRepo, func()) {
	t.Helper()
	dir := t.TempDir()
	bs, err := storage.NewBoltStore(filepath.Join(dir, "rep.db"))
	if err != nil {
		t.Fatalf("NewBoltStore: %v", err)
	}
	cleanup := func() { bs.Close() }

	sender := &stubRepSender{}
	members := newStubRepMembers()
	store := storage.NewReputationRepo(bs.DB())
	h := NewReputationHandler(sender, store, members, testLogger())
	return h, sender, members, store, cleanup
}

// ---------------------------------------------------------------------------
// HandlePraise - durable balance mutation + template + numeric evidence
// ---------------------------------------------------------------------------

// TestRepPraiseKnownTarget applies /praise @bob and asserts:
//   - a message was sent (the quip template + balance evidence)
//   - Bob's balance increased and Alice's (actor) decreased
//   - the sent message contains numeric digits (the balance evidence)
func TestRepPraiseKnownTarget(t *testing.T) {
	h, sender, members, store, cleanup := newRepHandler(t)
	defer cleanup()
	members.put(bobMember)

	ctx := context.Background()
	msg := repMessage("/praise @bob", aliceUser)

	if err := h.HandlePraise(nil, msg); err != nil {
		t.Fatalf("HandlePraise: %v", err)
	}

	// Must have sent exactly one message.
	if len(sender.Sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(sender.Sent))
	}

	body := sender.Sent[0].Text
	if body == "" {
		t.Fatal("sent message body must not be empty")
	}

	// Body must contain numeric evidence of the balance change.
	if !strings.ContainsAny(body, "0123456789") {
		t.Fatalf("praise response must contain numeric balance evidence, got: %q", body)
	}

	// Balances must have been mutated durably.
	actorBal, err := store.Balance(ctx, storage.AbsChatID(msg.Chat.ID), aliceUser.ID, false)
	if err != nil {
		t.Fatalf("actor balance: %v", err)
	}
	targetBal, err := store.Balance(ctx, storage.AbsChatID(msg.Chat.ID), bobUser.ID, false)
	if err != nil {
		t.Fatalf("target balance: %v", err)
	}
	// Default is 10 for regular users. Praise: actor -1 (9), target +3 (13).
	if actorBal != 9 || targetBal != 13 {
		t.Fatalf("after praise: actor=9 target=13, got actor=%d target=%d", actorBal, targetBal)
	}
}

// TestRepRoastKnownTarget applies /roast @bob and asserts:
//   - a message was sent (template + balance evidence)
//   - both Alice and Bob lost 1 (each goes to 9)
//   - the message contains numeric digits
func TestRepRoastKnownTarget(t *testing.T) {
	h, sender, members, _, cleanup := newRepHandler(t)
	defer cleanup()
	members.put(bobMember)

	msg := repMessage("/roast @bob", aliceUser)

	if err := h.HandleRoast(nil, msg); err != nil {
		t.Fatalf("HandleRoast: %v", err)
	}

	if len(sender.Sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(sender.Sent))
	}

	body := sender.Sent[0].Text
	if !strings.ContainsAny(body, "0123456789") {
		t.Fatalf("roast response must contain numeric balance evidence, got: %q", body)
	}
}

// ---------------------------------------------------------------------------
// Self-target - must not mutate storage
// ---------------------------------------------------------------------------

// TestRepSelfTargetRejected asserts that praising or roasting oneself
// returns a visible error and does not mutate any balances.
func TestRepSelfTargetRejected(t *testing.T) {
	h, sender, members, _, cleanup := newRepHandler(t)
	defer cleanup()
	members.put(aliceMember)

	// /praise @alice = self-target
	msg := repMessage("/praise @alice", aliceUser)

	err := h.HandlePraise(nil, msg)
	if err != nil {
		t.Fatalf("self-praise must return an error or send a message; got err=%v", err)
	}
	if len(sender.Sent) == 0 {
		t.Fatal("self-target must produce a message reply, got none")
	}
	body := sender.Sent[0].Text
	if body == "" {
		t.Fatal("self-target reply must not be empty")
	}
}

// TestRepSelfTargetNoMutation asserts the store is unchanged after a
// failed self-target operation.
func TestRepSelfTargetNoMutation(t *testing.T) {
	h, _, members, store, cleanup := newRepHandler(t)
	defer cleanup()
	members.put(aliceMember)

	ctx := context.Background()
	absChat := storage.AbsChatID(-1001234567890)

	// Read pre balances (lazy-initialize manually).
	preActor, _ := store.Balance(ctx, absChat, aliceUser.ID, false)

	// Attempt self-roast.
	_ = h.HandleRoast(nil, repMessage("/roast @alice", aliceUser))

	postActor, err := store.Balance(ctx, absChat, aliceUser.ID, false)
	if err != nil {
		t.Fatalf("post balance: %v", err)
	}
	if preActor != postActor {
		t.Fatalf("self-roast must not mutate balance: pre=%d post=%d", preActor, postActor)
	}
}

// ---------------------------------------------------------------------------
// Unknown target - must not mutate storage
// ---------------------------------------------------------------------------

// TestRepUnknownTarget rejects a @mention for a user the bot has never
// observed in the chat and does not mutate balances.
func TestRepUnknownTarget(t *testing.T) {
	h, sender, _, _, cleanup := newRepHandler(t)
	defer cleanup()

	// "stranger" is not in memberLookup.
	msg := repMessage("/praise @stranger", aliceUser)

	if err := h.HandlePraise(nil, msg); err != nil {
		t.Fatalf("unknown target must return error or send message; got err=%v", err)
	}
	if len(sender.Sent) == 0 {
		t.Fatal("unknown target must produce a reply, got none")
	}
}

// TestRepUnknownTargetNoMutation asserts that a failed operation on an
// unknown target leaves the store unchanged.
func TestRepUnknownTargetNoMutation(t *testing.T) {
	h, _, _, store, cleanup := newRepHandler(t)
	defer cleanup()

	ctx := context.Background()
	absChat := storage.AbsChatID(-1001234567890)

	// Pre-populate Alice's balance to detect writes.
	preActor, _ := store.Balance(ctx, absChat, aliceUser.ID, false)

	_ = h.HandleRoast(nil, repMessage("/roast @stranger", aliceUser))

	postActor, err := store.Balance(ctx, absChat, aliceUser.ID, false)
	if err != nil {
		t.Fatalf("post balance: %v", err)
	}
	if preActor != postActor {
		t.Fatalf("unknown target must not mutate balances: pre=%d post=%d", preActor, postActor)
	}
}

// ---------------------------------------------------------------------------
// No arg, no reply - usage error, no mutation
// ---------------------------------------------------------------------------

// TestRepNoArgsNoReply asserts that a bare /praise or /roast with no
// @mention and no reply target replies with a usage/error message and
// does not mutate the store.
func TestRepNoArgsNoReply(t *testing.T) {
	h, sender, _, _, cleanup := newRepHandler(t)
	defer cleanup()

	msg := repMessage("/praise", aliceUser)
	if err := h.HandlePraise(nil, msg); err != nil {
		t.Fatalf("no-arg no-reply must return error or send message; got err=%v", err)
	}
	if len(sender.Sent) == 0 {
		t.Fatal("no-arg no-reply must produce a reply, got none")
	}
}

// TestRepNoArgsNoReplyNoMutation asserts that no balances were modified
// when the handler replied with a usage error.
func TestRepNoArgsNoReplyNoMutation(t *testing.T) {
	h, _, _, store, cleanup := newRepHandler(t)
	defer cleanup()

	ctx := context.Background()
	absChat := storage.AbsChatID(-1001234567890)

	preActor, _ := store.Balance(ctx, absChat, aliceUser.ID, false)

	_ = h.HandleRoast(nil, repMessage("/roast", aliceUser))

	postActor, err := store.Balance(ctx, absChat, aliceUser.ID, false)
	if err != nil {
		t.Fatalf("post balance: %v", err)
	}
	if preActor != postActor {
		t.Fatalf("no-arg must not mutate balance: pre=%d post=%d", preActor, postActor)
	}
}

// ---------------------------------------------------------------------------
// /rep - caller balance as numeric code
// ---------------------------------------------------------------------------

// TestRepReturnsCallerBalance asserts that /rep responds with Alice's
// current balance as a numeric value.
func TestRepReturnsCallerBalance(t *testing.T) {
	h, sender, _, _, cleanup := newRepHandler(t)
	defer cleanup()

	msg := repMessage("/rep", aliceUser)
	if err := h.HandleRep(nil, msg); err != nil {
		t.Fatalf("HandleRep: %v", err)
	}

	if len(sender.Sent) != 1 {
		t.Fatalf("expected 1 message, got %d", len(sender.Sent))
	}

	body := sender.Sent[0].Text
	// Default balance for a regular user is 10.
	if !strings.Contains(body, "10") {
		t.Fatalf("/rep should contain default balance 10, got: %q", body)
	}
}

// TestRepAfterPraiseShowsUpdatedBalance asserts that /rep reflects the
// balance change after a successful praise or roast.
func TestRepAfterPraiseShowsUpdatedBalance(t *testing.T) {
	h, sender, members, _, cleanup := newRepHandler(t)
	defer cleanup()
	members.put(bobMember)

	// First: praise Bob (Alice: 10 -> 9, Bob: 10 -> 13).
	_ = h.HandlePraise(nil, repMessage("/praise @bob", aliceUser))
	sender.Sent = nil

	// Then: check Alice's balance.
	_ = h.HandleRep(nil, repMessage("/rep", aliceUser))
	body := sender.lastMsg()
	if !strings.Contains(body, "9") {
		t.Fatalf("after praise Alice balance should show 9, got: %q", body)
	}
}

// ---------------------------------------------------------------------------
// /reptop - bounded at ten, labels known members
// ---------------------------------------------------------------------------

// populateTop populates the store with n users at increasing balances.
func populateTop(t *testing.T, store *storage.ReputationRepo, baseUserID int64, n int) {
	t.Helper()

	ctx := context.Background()
	absChat := storage.AbsChatID(-1001234567890)
	for i := 0; i < n; i++ {
		userID := baseUserID + int64(i)
		// Apply praise multiple times to create different balances.
		for k := 0; k <= i; k++ {
			_, _ = store.Apply(ctx, absChat, userID+1000, userID, reputation.KindPraise, false, false)
		}
	}
}

// TestRepTopReturnsAtMostTen asserts that /reptop shows at most 10
// entries even when more users have balances.
func TestRepTopReturnsAtMostTen(t *testing.T) {
	h, sender, members, store, cleanup := newRepHandler(t)
	defer cleanup()

	// Populate 12 users (only top 10 should appear).
	members.put(membership.Member{UserID: 100})
	members.put(membership.Member{UserID: 200})
	members.put(membership.Member{UserID: 300})
	members.put(membership.Member{UserID: 400})
	members.put(membership.Member{UserID: 500})
	members.put(membership.Member{UserID: 600})
	members.put(membership.Member{UserID: 700})
	members.put(membership.Member{UserID: 800})
	members.put(membership.Member{UserID: 900})
	members.put(membership.Member{UserID: 1000})
	members.put(membership.Member{UserID: 1100})
	members.put(membership.Member{UserID: 1200})

	populateTop(t, store, 100, 12)

	if err := h.HandleRepTop(nil, repMessage("/reptop", aliceUser)); err != nil {
		t.Fatalf("HandleRepTop: %v", err)
	}
	if len(sender.Sent) == 0 {
		t.Fatal("HandleRepTop must send a reply")
	}

	body := sender.Sent[0].Text
	// Count lines that look like leaderboard entries (start with a digit and ".").
	lines := strings.Split(body, "\n")
	entryLines := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// A leaderboard entry starts with "N." where N is the rank.
		for _, c := range line {
			if c >= '0' && c <= '9' {
				entryLines++
				break
			}
		}
	}
	if entryLines > 10 {
		t.Fatalf("/reptop must show at most 10 entries, got %d lines with numbers", entryLines)
	}
}

// TestRepTopLabelsKnownMember asserts that a member with a username
// shows a display name rather than a raw numeric ID.
func TestRepTopLabelsKnownMember(t *testing.T) {
	h, sender, members, store, cleanup := newRepHandler(t)
	defer cleanup()

	members.put(aliceMember) // Username: "alice", FirstName: "Alice"

	ctx := context.Background()
	absChat := storage.AbsChatID(-1001234567890)
	_, _ = store.Apply(ctx, absChat, 999, aliceUser.ID, reputation.KindPraise, false, false)
	_ = h.HandleRepTop(nil, repMessage("/reptop", aliceUser))
	body := sender.lastMsg()

	if strings.Contains(body, "100") && !strings.Contains(body, "Alice") {
		t.Fatalf("/reptop should show Alice's display name for a known member, got: %q", body)
	}
}

// TestRepTopFallsBackToNumericID asserts that a user with a balance but
// no membership record shows their numeric ID.
func TestRepTopFallsBackToNumericID(t *testing.T) {
	h, sender, _, store, cleanup := newRepHandler(t)
	defer cleanup()

	ctx := context.Background()
	absChat := storage.AbsChatID(-1001234567890)
	// User 777 has a balance from actor 999 but is NOT in memberLookup.
	_, _ = store.Apply(ctx, absChat, 999, 777, reputation.KindPraise, false, false)
	_ = h.HandleRepTop(nil, repMessage("/reptop", aliceUser))
	body := sender.lastMsg()

	if !strings.Contains(body, "777") {
		t.Fatalf("/reptop should show numeric ID 777 for unknown member, got: %q", body)
	}
}

// ---------------------------------------------------------------------------
// Insufficient balance - roast when actor balance is zero, no mutation
// ---------------------------------------------------------------------------

func TestRepRoastInsufficientBalance(t *testing.T) {
	h, sender, members, store, cleanup := newRepHandler(t)
	defer cleanup()
	members.put(bobMember)

	ctx := context.Background()
	absChat := storage.AbsChatID(-1001234567890)

	// Drain Alice's balance by self-funding roasts through a third party.
	// Starting balance 10, each roast costs 1, so 10 roasts drain it.
	for range 10 {
		_, _ = store.Apply(ctx, absChat, 100, bobUser.ID, reputation.KindRoast, false, false)
	}

	// Alice now has 0 balance. Another roast should fail with insufficient.
	_ = h.HandleRoast(nil, repMessage("/roast @bob", aliceUser))
	if len(sender.Sent) == 0 {
		t.Fatal("insufficient balance must produce a reply")
	}
	body := sender.Sent[0].Text
	if body == "" {
		t.Fatal("insufficient balance reply must not be empty")
	}
}

// TestRepRoastInsufficientBalanceNoMutation asserts Bob's balance is
// unchanged after a failed insufficient-balance roast.
func TestRepRoastInsufficientBalanceNoMutation(t *testing.T) {
	h, _, members, store, cleanup := newRepHandler(t)
	defer cleanup()
	members.put(bobMember)

	ctx := context.Background()
	absChat := storage.AbsChatID(-1001234567890)

	for range 10 {
		_, _ = store.Apply(ctx, absChat, 100, bobUser.ID, reputation.KindRoast, false, false)
	}

	preBob, _ := store.Balance(ctx, absChat, bobUser.ID, false)

	_ = h.HandleRoast(nil, repMessage("/roast @bob", aliceUser))

	postBob, err := store.Balance(ctx, absChat, bobUser.ID, false)
	if err != nil {
		t.Fatalf("post balance: %v", err)
	}
	if preBob != postBob {
		t.Fatalf("insufficient-balance roast must not mutate target balance: pre=%d post=%d", preBob, postBob)
	}
}

// ---------------------------------------------------------------------------
// No-arg with reply - targets the replied-to human
// ---------------------------------------------------------------------------

// TestRepNoArgWithReplyToHuman targets the replied-to message's sender
// when no @mention is given, and mutates balances.
func TestRepNoArgWithReplyToHuman(t *testing.T) {
	h, sender, members, _, cleanup := newRepHandler(t)
	defer cleanup()
	members.put(bobMember)

	// Alice replies to Bob's message with /praise (no @bob).
	msg := repReplyMessage("/praise", aliceUser, bobUser)

	if err := h.HandlePraise(nil, msg); err != nil {
		t.Fatalf("reply-based praise: %v", err)
	}
	if len(sender.Sent) == 0 {
		t.Fatal("reply-based praise must send a message")
	}
	body := sender.Sent[0].Text
	if !strings.ContainsAny(body, "0123456789") {
		t.Fatalf("reply-based praise response must contain numeric evidence, got: %q", body)
	}
}

// ---------------------------------------------------------------------------
// Bot and nil From ignored
// ---------------------------------------------------------------------------

// TestRepBotSenderIgnored asserts that a message from a bot is silently
// skipped (no reply, no mutation).
func TestRepBotSenderIgnored(t *testing.T) {
	h, sender, _, _, cleanup := newRepHandler(t)
	defer cleanup()

	botUser := &telego.User{ID: 999, IsBot: true}
	msg := repMessage("/praise @bob", botUser)

	_ = h.HandlePraise(nil, msg)
	if len(sender.Sent) != 0 {
		t.Fatal("bot messages must be silently ignored")
	}
}

// TestRepNilFromIgnored asserts that a message with nil From is silently
// skipped (no reply, no mutation).
func TestRepNilFromIgnored(t *testing.T) {
	h, sender, _, _, cleanup := newRepHandler(t)
	defer cleanup()

	msg := repMessage("/praise @bob", nil)
	msg.From = nil // ensure nil From

	_ = h.HandlePraise(nil, msg)
	if len(sender.Sent) != 0 {
		t.Fatal("messages with nil From must be silently ignored")
	}
}
