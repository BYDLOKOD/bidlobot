package bot

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/domain/reputation"
	"github.com/veschin/bidlobot/internal/storage"
)

// ---------------------------------------------------------------------------
// Author index
// ---------------------------------------------------------------------------

func TestMsgAuthorIndexRecordLookup(t *testing.T) {
	idx := newMsgAuthorIndex(10)
	key := msgKey{chatID: -100, msgID: 42}
	if _, ok := idx.Lookup(key); ok {
		t.Fatal("lookup of unknown key must fail")
	}
	idx.Record(key, msgAuthor{userID: 7, username: "alice", firstName: "Alice"})
	a, ok := idx.Lookup(key)
	if !ok || a.userID != 7 || a.username != "alice" {
		t.Fatalf("lookup after record = %+v, %v", a, ok)
	}
}

func TestMsgAuthorIndexEvictsOldest(t *testing.T) {
	idx := newMsgAuthorIndex(2)
	idx.Record(msgKey{chatID: -100, msgID: 1}, msgAuthor{userID: 1})
	idx.Record(msgKey{chatID: -100, msgID: 2}, msgAuthor{userID: 2})
	idx.Record(msgKey{chatID: -100, msgID: 3}, msgAuthor{userID: 3}) // evicts 1

	if _, ok := idx.Lookup(msgKey{chatID: -100, msgID: 1}); ok {
		t.Fatal("oldest entry must be evicted")
	}
	if _, ok := idx.Lookup(msgKey{chatID: -100, msgID: 2}); !ok {
		t.Fatal("second entry must survive")
	}
	if _, ok := idx.Lookup(msgKey{chatID: -100, msgID: 3}); !ok {
		t.Fatal("newest entry must survive")
	}
}

func TestMsgAuthorIndexReRecordDoesNotGrow(t *testing.T) {
	idx := newMsgAuthorIndex(1)
	key := msgKey{chatID: -100, msgID: 1}
	idx.Record(key, msgAuthor{userID: 1})
	idx.Record(key, msgAuthor{userID: 2}) // refresh
	idx.Record(msgKey{chatID: -100, msgID: 2}, msgAuthor{userID: 3})
	// Capacity is 1; re-record must not have pushed a second FIFO entry,
	// so msgID 1 (refreshed) is evicted only by msgID 2.
	if _, ok := idx.Lookup(key); ok {
		t.Fatal("refreshed key must be evicted by newer message at capacity 1")
	}
}

// ---------------------------------------------------------------------------
// Emoji mapping
// ---------------------------------------------------------------------------

func TestRepEmojiToKind(t *testing.T) {
	cases := []struct {
		name      string
		emoji     string
		wantKind  reputation.Kind
		wantTrack bool
	}{
		{"like", emojiLike, reputation.KindPraise, true},
		{"dislike", emojiDislike, reputation.KindRoast, true},
		{"clown", emojiClown, reputation.KindRoast, true},
		{"untracked emoji", "🔥", 0, false},
		{"custom premium emoji", "", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var rs []telego.ReactionType
			if c.emoji != "" {
				rs = []telego.ReactionType{&telego.ReactionTypeEmoji{Emoji: c.emoji}}
			} else {
				rs = []telego.ReactionType{&telego.ReactionTypeCustomEmoji{CustomEmojiID: "5378452354"}}
			}
			kind, emoji, tracked := repEmojiToKind(rs)
			if tracked != c.wantTrack || (tracked && (kind != c.wantKind || emoji != c.emoji)) {
				t.Fatalf("repEmojiToKind(%q) = kind %v emoji %q tracked %v", c.emoji, kind, emoji, tracked)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// repReactor
// ---------------------------------------------------------------------------

// newRepReactorForTest builds a reactor with a real bbolt reputation repo,
// a controllable admin checker, and a recording sender.
func newRepReactorForTest(t *testing.T) (*repReactor, *stubRepSender, *storage.ReputationRepo, func()) {
	t.Helper()
	dir := t.TempDir()
	bs, err := storage.NewBoltStore(dir + "/rep.db")
	if err != nil {
		t.Fatalf("NewBoltStore: %v", err)
	}
	store := storage.NewReputationRepo(bs.DB())
	sender := &stubRepSender{}
	r := NewRepReactor(store, stubAdminCacheFunc(func(id int64) bool { return false }), sender, testLogger())
	return r, sender, store, func() { _ = bs.Close() }
}

// reactionFor builds a message_reaction update: reactor reacts with emoji
// to msgID in chat.
func reactionFor(reactor *telego.User, msgID int, emoji string) telego.MessageReactionUpdated {
	return telego.MessageReactionUpdated{
		Chat:      telego.Chat{ID: -1001234567890},
		MessageID: msgID,
		User:      reactor,
		NewReaction: []telego.ReactionType{
			&telego.ReactionTypeEmoji{Emoji: emoji},
		},
	}
}

func TestRepReactorPraiseCreditsAuthor(t *testing.T) {
	r, _, store, cleanup := newRepReactorForTest(t)
	defer cleanup()
	// Alice writes message 5.
	r.RecordMessage(&telego.Message{
		MessageID: 5,
		Chat:      telego.Chat{ID: -1001234567890},
		From:      aliceUser,
	})
	// Bob likes it.
	if err := r.Handle(nil, reactionFor(bobUser, 5, emojiLike)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	bal, err := store.Balance(context.Background(), 1001234567890, aliceUser.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if bal != 10+3 {
		t.Fatalf("alice balance after praise = %d, want %d", bal, 10+3)
	}
}

func TestRepReactorRoastDebitsAuthor(t *testing.T) {
	for _, emoji := range []string{emojiDislike, emojiClown} {
		t.Run(emoji, func(t *testing.T) {
			r, _, store, cleanup := newRepReactorForTest(t)
			defer cleanup()
			r.RecordMessage(&telego.Message{
				MessageID: 7,
				Chat:      telego.Chat{ID: -1001234567890},
				From:      aliceUser,
			})
			if err := r.Handle(nil, reactionFor(bobUser, 7, emoji)); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			bal, err := store.Balance(context.Background(), 1001234567890, aliceUser.ID, false)
			if err != nil {
				t.Fatal(err)
			}
			if bal != 10-1 {
				t.Fatalf("alice balance after roast = %d, want %d", bal, 10-1)
			}
		})
	}
}

func TestRepReactorSkipsSelfReaction(t *testing.T) {
	r, _, store, cleanup := newRepReactorForTest(t)
	defer cleanup()
	r.RecordMessage(&telego.Message{
		MessageID: 9,
		Chat:      telego.Chat{ID: -1001234567890},
		From:      aliceUser,
	})
	_ = r.Handle(nil, reactionFor(aliceUser, 9, emojiLike))
	bal, _ := store.Balance(context.Background(), 1001234567890, aliceUser.ID, false)
	if bal != 10 {
		t.Fatalf("self-reaction must not change balance, got %d", bal)
	}
}

func TestRepReactorSkipsAnonymousAndBotReactors(t *testing.T) {
	r, _, store, cleanup := newRepReactorForTest(t)
	defer cleanup()
	r.RecordMessage(&telego.Message{
		MessageID: 11,
		Chat:      telego.Chat{ID: -1001234567890},
		From:      aliceUser,
	})
	// Anonymous: User == nil, ActorChat set.
	anon := telego.MessageReactionUpdated{
		Chat:      telego.Chat{ID: -1001234567890},
		MessageID: 11,
		ActorChat: &telego.Chat{ID: -1001234567890},
		NewReaction: []telego.ReactionType{
			&telego.ReactionTypeEmoji{Emoji: emojiLike},
		},
	}
	if err := r.Handle(nil, anon); err != nil {
		t.Fatalf("Handle anonymous: %v", err)
	}
	// Bot reactor.
	bot := &telego.User{ID: 999, IsBot: true, Username: "somebot"}
	if err := r.Handle(nil, reactionFor(bot, 11, emojiLike)); err != nil {
		t.Fatalf("Handle bot: %v", err)
	}
	bal, _ := store.Balance(context.Background(), 1001234567890, aliceUser.ID, false)
	if bal != 10 {
		t.Fatalf("anonymous/bot reactions must not change balance, got %d", bal)
	}
}

func TestRepReactorSkipsUnknownMessage(t *testing.T) {
	r, _, store, cleanup := newRepReactorForTest(t)
	defer cleanup()
	// No RecordMessage for message 13.
	_ = r.Handle(nil, reactionFor(bobUser, 13, emojiLike))
	bal, _ := store.Balance(context.Background(), 1001234567890, aliceUser.ID, false)
	if bal != 10 {
		t.Fatalf("unattributed reaction must not change balance, got %d", bal)
	}
}

func TestRepReactorSkipsBotAuthorMessage(t *testing.T) {
	r, _, store, cleanup := newRepReactorForTest(t)
	defer cleanup()
	botAuthor := &telego.User{ID: 999, IsBot: true, Username: "somebot"}
	r.RecordMessage(&telego.Message{
		MessageID: 15,
		Chat:      telego.Chat{ID: -1001234567890},
		From:      botAuthor,
	})
	_ = r.Handle(nil, reactionFor(bobUser, 15, emojiLike))
	bal, _ := store.Balance(context.Background(), 1001234567890, bobUser.ID, false)
	if bal != 10 {
		t.Fatalf("reaction on bot message must not debit reactor, got %d", bal)
	}
}

func TestRepReactorRemovalDoesNotReverse(t *testing.T) {
	r, _, store, cleanup := newRepReactorForTest(t)
	defer cleanup()
	r.RecordMessage(&telego.Message{
		MessageID: 17,
		Chat:      telego.Chat{ID: -1001234567890},
		From:      aliceUser,
	})
	// Bob likes.
	_ = r.Handle(nil, reactionFor(bobUser, 17, emojiLike))
	// Bob removes the reaction: NewReaction empty.
	removed := telego.MessageReactionUpdated{
		Chat:        telego.Chat{ID: -1001234567890},
		MessageID:   17,
		User:        bobUser,
		OldReaction: []telego.ReactionType{&telego.ReactionTypeEmoji{Emoji: emojiLike}},
	}
	if err := r.Handle(nil, removed); err != nil {
		t.Fatalf("Handle removal: %v", err)
	}
	bal, _ := store.Balance(context.Background(), 1001234567890, aliceUser.ID, false)
	if bal != 13 {
		t.Fatalf("removal must not reverse the praise, want 13 got %d", bal)
	}
}

func TestRepReactorAdminTargetGetsSix(t *testing.T) {
	r, _, store, cleanup := newRepReactorForTest(t)
	defer cleanup()
	r.admins = stubAdminCacheFunc(func(id int64) bool { return id == aliceUser.ID })
	r.RecordMessage(&telego.Message{
		MessageID: 19,
		Chat:      telego.Chat{ID: -1001234567890},
		From:      aliceUser, // alice is admin
	})
	_ = r.Handle(nil, reactionFor(bobUser, 19, emojiLike))
	bal, _ := store.Balance(context.Background(), 1001234567890, aliceUser.ID, false)
	if bal != 20+6 {
		t.Fatalf("admin target praise = %d, want %d", bal, 20+6)
	}
}

// ---------------------------------------------------------------------------
// repBatcher
// ---------------------------------------------------------------------------

// stubRepSender is already defined in reputation_test.go; this helper
// waits until n messages have been sent.
func waitForSends(t *testing.T, s *stubRepSender, n int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		got := len(s.Sent)
		s.mu.Unlock()
		if got >= n {
			s.mu.Lock()
			defer s.mu.Unlock()
			var texts []string
			for _, p := range s.Sent {
				texts = append(texts, p.Text)
			}
			return texts
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t.Fatalf("timed out waiting for %d sends, got %d", n, len(s.Sent))
	return nil
}

func TestRepBatcherFlushesCombinedMessage(t *testing.T) {
	sender := &stubRepSender{}
	r := newRepBatcher(sender, testLogger())
	r.window = 30 * time.Millisecond

	r.Add(-100, repEvent{emoji: emojiLike, delta: 3, target: "alice"})
	r.Add(-100, repEvent{emoji: emojiDislike, delta: -1, target: "bob"})
	texts := waitForSends(t, sender, 1)

	if !strings.Contains(texts[0], "Репутация:") {
		t.Fatalf("combined message must have header, got %q", texts[0])
	}
	if !strings.Contains(texts[0], "👍 alice +3") || !strings.Contains(texts[0], "👎 bob -1") {
		t.Fatalf("combined message missing entries, got %q", texts[0])
	}
}

func TestRepBatcherSplitsWindows(t *testing.T) {
	sender := &stubRepSender{}
	r := newRepBatcher(sender, testLogger())
	r.window = 30 * time.Millisecond

	r.Add(-100, repEvent{emoji: emojiLike, delta: 3, target: "alice"})
	texts := waitForSends(t, sender, 1)
	if !strings.Contains(texts[0], "alice") {
		t.Fatalf("first window message wrong: %q", texts[0])
	}

	// Second event lands after the window flushed -> new window, new message.
	time.Sleep(60 * time.Millisecond)
	r.Add(-100, repEvent{emoji: emojiLike, delta: 3, target: "carol"})
	texts = waitForSends(t, sender, 2)
	if !strings.Contains(texts[1], "carol") {
		t.Fatalf("second window message wrong: %q", texts[1])
	}
}

func TestRepBatcherSeparatesChats(t *testing.T) {
	sender := &stubRepSender{}
	r := newRepBatcher(sender, testLogger())
	r.window = 30 * time.Millisecond

	r.Add(-100, repEvent{emoji: emojiLike, delta: 3, target: "alice"})
	r.Add(-200, repEvent{emoji: emojiDislike, delta: -1, target: "bob"})
	texts := waitForSends(t, sender, 2)
	if len(texts) != 2 {
		t.Fatalf("two chats must produce two messages, got %d: %v", len(texts), texts)
	}
}

// ---------------------------------------------------------------------------
// Integration: fanout path
// ---------------------------------------------------------------------------

func TestRepReactorHandleBatchesIntoFanout(t *testing.T) {
	// Verify the reactor feeds its batcher: two reactions in the same
	// window produce one combined message via the sender.
	r, sender, _, cleanup := newRepReactorForTest(t)
	defer cleanup()
	r.batch.window = 30 * time.Millisecond

	r.RecordMessage(&telego.Message{
		MessageID: 21,
		Chat:      telego.Chat{ID: -1001234567890},
		From:      aliceUser,
	})
	r.RecordMessage(&telego.Message{
		MessageID: 22,
		Chat:      telego.Chat{ID: -1001234567890},
		From:      bobUser,
	})
	_ = r.Handle(nil, reactionFor(carolUser, 21, emojiLike))
	_ = r.Handle(nil, reactionFor(daveUser, 22, emojiLike))

	texts := waitForSends(t, sender, 1)
	if !strings.Contains(texts[0], "alice") || !strings.Contains(texts[0], "bob") {
		t.Fatalf("batcher must combine both reactions, got %q", texts[0])
	}
}

// silence the unused-import guard for sync in case stubs change.
var _ = sync.Mutex{}
