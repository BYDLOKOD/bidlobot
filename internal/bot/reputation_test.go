package bot

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/domain/reputation"
	"github.com/veschin/bidlobot/internal/storage"
)

// stubRepSender records every SendMessage params for assertion.
type stubRepSender struct {
	mu      sync.Mutex
	Sent    []*telego.SendMessageParams
	SendErr error

	// chatMembers maps userID -> a telego.ChatMember that GetChatMember
	// returns. A missing entry falls back to DefaultMember; nil leaves
	// the lookup unconfigured and the handler fail-closes.
	chatMembers   map[int64]telego.ChatMember
	DefaultMember telego.ChatMember
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

func (s *stubRepSender) GetChatMember(_ context.Context, params *telego.GetChatMemberParams) (telego.ChatMember, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.chatMembers != nil {
		if cm, ok := s.chatMembers[params.UserID]; ok {
			return cm, nil
		}
	}
	return s.DefaultMember, nil
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

	aliceMember = membership.Member{UserID: 100, Username: "alice", FirstName: "Alice", Status: membership.StatusMember}
	bobMember   = membership.Member{UserID: 200, Username: "bob", FirstName: "Bob", Status: membership.StatusMember}
	carolMember = membership.Member{UserID: 300, Username: "carol", FirstName: "Carol", Status: membership.StatusMember}
	daveMember  = membership.Member{UserID: 400, Username: "dave", FirstName: "Dave", Status: membership.StatusMember}
)

var repTestChatID int64 = -1001234567890

// newRepHandler creates a ReputationHandler with real bbolt-backed storage
// and in-memory membership stubs.
func newRepHandler(t *testing.T) (*ReputationHandler, *stubRepSender, *stubRepMembers, *storage.ReputationRepo, *bolt.DB, func()) {
	t.Helper()
	dir := t.TempDir()
	bs, err := storage.NewBoltStore(filepath.Join(dir, "rep.db"))
	if err != nil {
		t.Fatalf("NewBoltStore: %v", err)
	}
	cleanup := func() { _ = bs.Close() }

	sender := &stubRepSender{DefaultMember: &telego.ChatMemberMember{Status: "member"}}
	members := newStubRepMembers()
	store := storage.NewReputationRepo(bs.DB())
	h := NewReputationHandler(sender, store, members, testLogger())
	return h, sender, members, store, bs.DB(), cleanup
}

var repBaseDay = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

// seedPraise performs one durable praise without going through the
// handler, used to arrange state that the daily limits would otherwise
// make slow to build.
func seedPraise(t *testing.T, store *storage.ReputationRepo, chatID, actorID, targetID int64, day, messageID int) {
	t.Helper()
	_, err := store.Settle(context.Background(), reputation.SocialRequest{
		ChatID: chatID, ActorID: actorID, TargetID: targetID, Kind: reputation.KindPraise,
		Source: reputation.SourceSlash, SourceMessageID: messageID,
		Now: repBaseDay.AddDate(0, 0, day),
	})
	if err != nil {
		t.Fatalf("seed praise: %v", err)
	}
}

// seedScore raises a target's title score with distinct actors, so the
// per-pair and per-sender limits stay satisfied. score must be a multiple
// of three.
func seedScore(t *testing.T, store *storage.ReputationRepo, chatID, targetID int64, score int, actorBase int64) {
	t.Helper()
	for i := range score / 3 {
		seedPraise(t, store, chatID, actorBase+int64(i), targetID, 0, int(actorBase)+i)
	}
}

// ---------------------------------------------------------------------------
// Praise / roast settlement
// ---------------------------------------------------------------------------

func TestRepPraiseKnownTarget(t *testing.T) {
	h, sender, members, store, _, cleanup := newRepHandler(t)
	defer cleanup()
	members.put(bobMember)

	if err := h.HandlePraise(nil, repMessage("/praise @bob", aliceUser)); err != nil {
		t.Fatalf("HandlePraise: %v", err)
	}
	if len(sender.Sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(sender.Sent))
	}
	body := sender.Sent[0].Text
	if !strings.Contains(body, "+3") || !strings.Contains(body, "-1") {
		t.Fatalf("receipt must state the signed change, got %q", body)
	}
	if strings.Contains(body, "@") {
		t.Fatalf("public receipt must not ping, got %q", body)
	}

	actor := mustWallet(t, store, repTestChatID, aliceUser.ID)
	target := mustWallet(t, store, repTestChatID, bobUser.ID)
	if actor.Available != 9 || actor.Score != 0 {
		t.Fatalf("actor = %+v", actor)
	}
	if target.Available != 13 || target.Score != 3 {
		t.Fatalf("target = %+v", target)
	}
}

func TestRepReasonCatalogueIntegration(t *testing.T) {
	repCopy.reset()
	h, sender, members, _, _, cleanup := newRepHandler(t)
	defer cleanup()
	members.put(bobMember)

	if err := h.HandlePraise(nil, repMessage("/praise review @bob", aliceUser)); err != nil {
		t.Fatal(err)
	}
	body := sender.lastMsg()
	if !containsLine(body, praiseReview) {
		t.Fatalf("review reason must select a review line, got %q", body)
	}
	if containsLine(body, praiseGeneric) || containsLine(body, praiseHelp) || containsLine(body, praiseExplanation) {
		t.Fatalf("review reason leaked another family: %q", body)
	}

	// A generic command never selects a reason-specific line.
	repCopy.reset()
	h2, sender2, members2, _, _, cleanup2 := newRepHandler(t)
	defer cleanup2()
	members2.put(bobMember)
	_ = h2.HandlePraise(nil, repMessage("/praise @bob", aliceUser))
	generic := sender2.lastMsg()
	if !containsLine(generic, praiseGeneric) {
		t.Fatalf("generic praise must use lines 1-16, got %q", generic)
	}
	if containsLine(generic, praiseHelp) || containsLine(generic, praiseExplanation) || containsLine(generic, praiseReview) {
		t.Fatalf("generic praise used a reason-specific line: %q", generic)
	}
}

func TestRepRoastKnownTarget(t *testing.T) {
	h, sender, members, store, _, cleanup := newRepHandler(t)
	defer cleanup()
	members.put(bobMember)

	if err := h.HandleRoast(nil, repMessage("/roast @bob", aliceUser)); err != nil {
		t.Fatal(err)
	}
	body := sender.lastMsg()
	if !strings.Contains(body, "-1") {
		t.Fatalf("roast receipt must state the change, got %q", body)
	}
	actor := mustWallet(t, store, repTestChatID, aliceUser.ID)
	target := mustWallet(t, store, repTestChatID, bobUser.ID)
	if actor.Available != 9 || target.Available != 9 || target.Score != 0 {
		t.Fatalf("actor=%+v target=%+v", actor, target)
	}
}

func TestRepAdminParity(t *testing.T) {
	h, sender, members, _, _, cleanup := newRepHandler(t)
	defer cleanup()
	members.put(bobMember)
	h = NewReputationHandler(h.bot, h.store, h.members, testLogger(),
		stubAdminCacheFunc(func(id int64) bool { return id == bobUser.ID }))

	_ = h.HandlePraise(nil, repMessage("/praise @bob", aliceUser))
	body := sender.lastMsg()
	if !strings.Contains(body, "+3") || strings.Contains(body, "+6") {
		t.Fatalf("administrator must receive the same +3, got %q", body)
	}
	if !strings.Contains(body, "Счёт: bob \u2014 3") {
		t.Fatalf("admin title score must also be +3, got %q", body)
	}
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

func TestRepSelfTargetRejected(t *testing.T) {
	h, sender, members, store, _, cleanup := newRepHandler(t)
	defer cleanup()
	members.put(aliceMember)

	if err := h.HandlePraise(nil, repMessage("/praise @alice", aliceUser)); err != nil {
		t.Fatal(err)
	}
	if got := sender.lastMsg(); !strings.Contains(got, "себе нельзя") {
		t.Fatalf("self-target copy = %q", got)
	}
	if got := mustWallet(t, store, repTestChatID, aliceUser.ID); got.Available != 10 || got.Score != 0 {
		t.Fatalf("self-target mutated wallet: %+v", got)
	}
}

func TestRepUnknownTargetNoMutation(t *testing.T) {
	h, sender, _, store, _, cleanup := newRepHandler(t)
	defer cleanup()

	_ = h.HandleRoast(nil, repMessage("/roast @stranger", aliceUser))
	if len(sender.Sent) == 0 {
		t.Fatal("unknown target must produce a reply")
	}
	if got := mustWallet(t, store, repTestChatID, aliceUser.ID); got.Available != 10 {
		t.Fatalf("unknown target mutated wallet: %+v", got)
	}
}

func TestRepNoArgsNoMutation(t *testing.T) {
	h, sender, _, store, _, cleanup := newRepHandler(t)
	defer cleanup()

	_ = h.HandlePraise(nil, repMessage("/praise", aliceUser))
	if !strings.Contains(sender.lastMsg(), "нужно ответить") {
		t.Fatalf("missing-target copy = %q", sender.lastMsg())
	}
	if got := mustWallet(t, store, repTestChatID, aliceUser.ID); got.Available != 10 {
		t.Fatalf("missing target mutated wallet: %+v", got)
	}
}

func TestRepBotSenderAndNilFromIgnored(t *testing.T) {
	h, sender, _, _, _, cleanup := newRepHandler(t)
	defer cleanup()

	_ = h.HandlePraise(nil, repMessage("/praise @bob", &telego.User{ID: 999, IsBot: true}))
	msg := repMessage("/praise @bob", nil)
	msg.From = nil
	_ = h.HandlePraise(nil, msg)
	if len(sender.Sent) != 0 {
		t.Fatal("bot and nil senders must be silently ignored")
	}
}

func TestRepTargetBotRejected(t *testing.T) {
	h, sender, members, _, _, cleanup := newRepHandler(t)
	defer cleanup()
	members.put(membership.Member{UserID: bobUser.ID, Username: "bob", FirstName: "Bob", IsBot: true, Status: membership.StatusMember})

	_ = h.HandlePraise(nil, repMessage("/praise @bob", aliceUser))
	if got := sender.lastMsg(); !strings.Contains(got, "ботам репутация не нужна") {
		t.Fatalf("bot target copy = %q", got)
	}
}

func TestRepNoArgWithReplyToHuman(t *testing.T) {
	h, sender, members, store, _, cleanup := newRepHandler(t)
	defer cleanup()
	members.put(bobMember)

	_ = h.HandlePraise(nil, repReplyMessage("/praise", aliceUser, bobUser))
	if len(sender.Sent) == 0 {
		t.Fatal("reply-based praise must send a message")
	}
	if got := mustWallet(t, store, repTestChatID, bobUser.ID); got.Score != 3 {
		t.Fatalf("reply-based praise score = %+v", got)
	}
}

// ---------------------------------------------------------------------------
// Live membership
// ---------------------------------------------------------------------------

func TestRepLiveMembershipRejectsDepartedTarget(t *testing.T) {
	for _, status := range []string{"left", "kicked", "restricted-not-member"} {
		t.Run(status, func(t *testing.T) {
			h, sender, members, store, _, cleanup := newRepHandler(t)
			defer cleanup()
			members.put(bobMember)
			sender.chatMembers = map[int64]telego.ChatMember{
				bobUser.ID: repMember(status),
			}
			_ = h.HandlePraise(nil, repReplyMessage("/praise", aliceUser, bobUser))
			if got := sender.lastMsg(); !strings.Contains(got, "его уже нет в чате") {
				t.Fatalf("departed-target copy = %q", got)
			}
			if got := mustWallet(t, store, repTestChatID, bobUser.ID); got.Score != 0 || got.Available != 10 {
				t.Fatalf("departed target mutated: %+v", got)
			}
		})
	}
}

func TestRepLiveMembershipRejectsDepartedActor(t *testing.T) {
	h, sender, members, store, _, cleanup := newRepHandler(t)
	defer cleanup()
	members.put(bobMember)
	sender.chatMembers = map[int64]telego.ChatMember{
		aliceUser.ID: repMember("left"),
	}
	_ = h.HandlePraise(nil, repMessage("/praise @bob", aliceUser))
	if got := sender.lastMsg(); !strings.Contains(got, "не удалось проверить твоё участие") {
		t.Fatalf("departed-actor copy = %q", got)
	}
	if got := mustWallet(t, store, repTestChatID, bobUser.ID); got.Score != 0 {
		t.Fatalf("departed actor mutated target: %+v", got)
	}
}

func TestRepLiveMembershipAcceptsRestrictedMember(t *testing.T) {
	h, sender, members, store, _, cleanup := newRepHandler(t)
	defer cleanup()
	members.put(bobMember)
	sender.chatMembers = map[int64]telego.ChatMember{
		bobUser.ID: &telego.ChatMemberRestricted{Status: "restricted", IsMember: true},
	}
	_ = h.HandlePraise(nil, repMessage("/praise @bob", aliceUser))
	if strings.Contains(sender.lastMsg(), "его уже нет в чате") {
		t.Fatalf("restricted-but-member must not be rejected: %q", sender.lastMsg())
	}
	if got := mustWallet(t, store, repTestChatID, bobUser.ID); got.Score != 3 {
		t.Fatalf("restricted member payout = %+v", got)
	}
}

// ---------------------------------------------------------------------------
// /rep and /reptop
// ---------------------------------------------------------------------------

func TestRepShowsWalletTitleAndDistance(t *testing.T) {
	h, sender, _, _, db, cleanup := newRepHandler(t)
	defer cleanup()

	if err := db.Update(func(tx *bolt.Tx) error {
		return storage.AdjustReputationWallet(tx, repTestChatID, aliceUser.ID, -4, 4)
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.HandleRep(nil, repMessage("/rep", aliceUser)); err != nil {
		t.Fatal(err)
	}
	body := sender.lastMsg()
	for _, want := range []string{
		"Доступно: <b>6</b>",
		"В резерве: <b>4</b>",
		"Очки: <b>0</b>\nЗвание: Участник",
		"Следующее звание: Дежурный. Осталось очков: <b>12</b>.",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("/rep missing %q in %q", want, body)
		}
	}
	if strings.Contains(body, "@") {
		t.Fatalf("/rep must not ping: %q", body)
	}
}

func TestRepTopByScoreWithTitlesAndObservedMembers(t *testing.T) {
	h, sender, members, store, _, cleanup := newRepHandler(t)
	defer cleanup()

	// 110 and 111 outrank the rest; unknown 999 is scored but has no
	// observed membership record.
	seedScore(t, store, repTestChatID, 110, 12, 5000)
	seedScore(t, store, repTestChatID, 111, 6, 5100)
	seedScore(t, store, repTestChatID, 999, 3, 5200)
	actorBase := int64(6000)
	for userID := int64(100); userID <= 109; userID++ {
		seedScore(t, store, repTestChatID, userID, 3, actorBase)
		actorBase += 10
		members.put(membership.Member{
			UserID:    userID,
			Username:  "u" + itoa(userID),
			FirstName: "U" + itoa(userID),
			Status:    membership.StatusMember,
		})
	}
	members.put(membership.Member{UserID: 110, Username: "u110", FirstName: "U110", Status: membership.StatusMember})
	members.put(membership.Member{UserID: 111, Username: "u111", FirstName: "U111", Status: membership.StatusMember})

	if err := h.HandleRepTop(nil, repMessage("/reptop", aliceUser)); err != nil {
		t.Fatal(err)
	}
	body := sender.lastMsg()
	if strings.Contains(body, "@") {
		t.Fatalf("/reptop must not ping: %q", body)
	}
	if strings.Contains(body, "999") {
		t.Fatalf("/reptop listed an unobserved member: %q", body)
	}
	lines := strings.Split(body, "\n")
	if len(lines) != 12 || !strings.HasPrefix(lines[1], "<pre>") {
		t.Fatalf("expected a heading, column labels and ten rows: %q", body)
	}
	if !strings.Contains(lines[2], "u110") || !strings.Contains(lines[2], "12") || !strings.Contains(lines[2], "Дежурный") {
		t.Fatalf("first entry = %q", lines[2])
	}
	if strings.Contains(body, "открывать инцидент") {
		t.Fatalf("the table must not repeat game instructions: %q", body)
	}
	if !strings.Contains(lines[3], "u111") || !strings.Contains(lines[3], " 6 ") {
		t.Fatalf("second entry = %q", lines[3])
	}
	if !strings.Contains(lines[4], "u100") {
		t.Fatalf("tie must fall back to user id ascending, third entry = %q", lines[4])
	}
}

func TestRepTopEmpty(t *testing.T) {
	h, sender, _, _, _, cleanup := newRepHandler(t)
	defer cleanup()
	_ = h.HandleRepTop(nil, repMessage("/reptop", aliceUser))
	if got := sender.lastMsg(); !strings.Contains(got, "Ни у кого пока нет очков.") {
		t.Fatalf("empty leaderboard copy = %q", got)
	}
}

func TestRepHighestTitle(t *testing.T) {
	h, sender, _, store, _, cleanup := newRepHandler(t)
	defer cleanup()
	seedScore(t, store, repTestChatID, aliceUser.ID, 72, 8000)
	_ = h.HandleRep(nil, repMessage("/rep", aliceUser))
	body := sender.lastMsg()
	if !strings.Contains(body, "Звание: Хранитель прода") || strings.Contains(body, "Следующее звание:") {
		t.Fatalf("highest title copy = %q", body)
	}
}

// ---------------------------------------------------------------------------
// Limits and duplicate-source copy
// ---------------------------------------------------------------------------

func TestRepDailyLimitsCopy(t *testing.T) {
	t.Run("pair", func(t *testing.T) {
		h, sender, members, store, _, cleanup := newRepHandler(t)
		defer cleanup()
		members.put(bobMember)
		seedPraise(t, store, repTestChatID, aliceUser.ID, bobUser.ID, 0, 1)

		msg := repMessage("/praise @bob", aliceUser)
		msg.MessageID = 99
		_ = h.HandlePraise(nil, msg)
		if got := sender.lastMsg(); !strings.Contains(got, "с этой парой уже считались сегодня") {
			t.Fatalf("pair-limit copy = %q", got)
		}
	})

	t.Run("sender", func(t *testing.T) {
		h, sender, members, store, _, cleanup := newRepHandler(t)
		defer cleanup()
		members.put(carolMember)
		seedPraise(t, store, repTestChatID, aliceUser.ID, 201, 0, 1)
		seedPraise(t, store, repTestChatID, aliceUser.ID, 202, 0, 2)
		seedPraise(t, store, repTestChatID, aliceUser.ID, 203, 0, 3)

		msg := repMessage("/praise @carol", aliceUser)
		msg.MessageID = 99
		_ = h.HandlePraise(nil, msg)
		if got := sender.lastMsg(); !strings.Contains(got, "три пересчёта в сутки") {
			t.Fatalf("sender-limit copy = %q", got)
		}
	})

	t.Run("duplicate source", func(t *testing.T) {
		h, sender, members, _, _, cleanup := newRepHandler(t)
		defer cleanup()
		members.put(bobMember)
		msg := repMessage("/praise @bob", aliceUser)
		_ = h.HandlePraise(nil, msg)
		_ = h.HandlePraise(nil, msg)
		if got := sender.lastMsg(); !strings.Contains(got, "этот плюс уже учтён") {
			t.Fatalf("duplicate-source copy = %q", got)
		}
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func mustWallet(t *testing.T, store *storage.ReputationRepo, chatID, userID int64) reputation.Account {
	t.Helper()
	account, err := store.Wallet(context.Background(), chatID, userID)
	if err != nil {
		t.Fatalf("Wallet: %v", err)
	}
	return account
}

func containsLine(body string, lines []string) bool {
	for _, line := range lines {
		if strings.Contains(body, line) {
			return true
		}
	}
	return false
}

func itoa(v int64) string {
	var buf [20]byte
	i := len(buf)
	if v == 0 {
		return "0"
	}
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// repMember builds a telego.ChatMember that reports the requested status.
func repMember(status string) telego.ChatMember {
	switch status {
	case "left", "kicked":
		return &telego.ChatMemberLeft{Status: status}
	case "restricted-not-member":
		return &telego.ChatMemberRestricted{Status: "restricted", IsMember: false}
	case "administrator":
		return &telego.ChatMemberAdministrator{Status: status}
	case "owner":
		return &telego.ChatMemberOwner{Status: status}
	default:
		return &telego.ChatMemberMember{Status: "member"}
	}
}

// stubAdminCacheFunc is a test AdminChecker whose result is decided
// by a caller-supplied function.
type stubAdminCacheFunc func(userID int64) bool

func (f stubAdminCacheFunc) IsAdmin(_ int64, userID int64) (bool, error) {
	return f(userID), nil
}
