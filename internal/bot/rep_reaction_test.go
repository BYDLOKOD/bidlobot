package bot

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
			kind, tracked := repEmojiToKind(rs)
			if tracked != c.wantTrack || (tracked && kind != c.wantKind) {
				t.Fatalf("repEmojiToKind(%q) = kind %v tracked %v", c.emoji, kind, tracked)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// repReactor
// ---------------------------------------------------------------------------

// newRepReactorForTest builds a reactor with a real bbolt reputation repo,
// a live membership stub and a recording sender.
func newRepReactorForTest(t *testing.T) (*repReactor, *stubRepSender, *storage.ReputationRepo, func()) {
	t.Helper()
	dir := t.TempDir()
	bs, err := storage.NewBoltStore(dir + "/rep.db")
	if err != nil {
		t.Fatalf("NewBoltStore: %v", err)
	}
	store := storage.NewReputationRepo(bs.DB())
	sender := &stubRepSender{DefaultMember: &telego.ChatMemberMember{Status: "member"}}
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

func reactionWallet(t *testing.T, store *storage.ReputationRepo, userID int64) reputation.Account {
	t.Helper()
	account, err := store.Wallet(context.Background(), 1001234567890, userID)
	if err != nil {
		t.Fatal(err)
	}
	return account
}

func TestRepReactorPraiseCreditsAuthor(t *testing.T) {
	r, _, store, cleanup := newRepReactorForTest(t)
	defer cleanup()
	r.RecordMessage(&telego.Message{
		MessageID: 5,
		Chat:      telego.Chat{ID: -1001234567890},
		From:      aliceUser,
	})
	if err := r.Handle(nil, reactionFor(bobUser, 5, emojiLike)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	alice := reactionWallet(t, store, aliceUser.ID)
	bob := reactionWallet(t, store, bobUser.ID)
	if alice.Available != 13 || alice.Score != 3 {
		t.Fatalf("alice = %+v, want 13/3", alice)
	}
	if bob.Available != 9 {
		t.Fatalf("bob = %+v, want 9", bob)
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
			alice := reactionWallet(t, store, aliceUser.ID)
			if alice.Available != 9 || alice.Score != 0 {
				t.Fatalf("alice after roast = %+v, want 9/0", alice)
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
	if got := reactionWallet(t, store, aliceUser.ID); got.Available != 10 || got.Score != 0 {
		t.Fatalf("self-reaction mutated wallet: %+v", got)
	}
}

func TestRepReactorCreditsOwnerOfBotRepost(t *testing.T) {
	r, _, store, cleanup := newRepReactorForTest(t)
	defer cleanup()
	r.RecordOwner(ownerRecord{ChatID: -1001234567890, MsgID: 21, Owner: aliceUser, Text: "ловите видос"})
	if err := r.Handle(nil, reactionFor(bobUser, 21, emojiLike)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	alice := reactionWallet(t, store, aliceUser.ID)
	if alice.Available != 13 || alice.Score != 3 {
		t.Fatalf("alice after like on bot repost = %+v", alice)
	}
	_ = r.Handle(nil, reactionFor(aliceUser, 21, emojiLike))
	if got := reactionWallet(t, store, aliceUser.ID); got.Available != 13 || got.Score != 3 {
		t.Fatalf("self-like on own repost must not change wallet: %+v", got)
	}
}

func TestRepReactorRecordOwnerNilSafety(t *testing.T) {
	var r *repReactor
	r.RecordOwner(ownerRecord{ChatID: 1, MsgID: 2, Owner: aliceUser})
	r2, _, _, cleanup := newRepReactorForTest(t)
	defer cleanup()
	r2.RecordOwner(ownerRecord{ChatID: 1, MsgID: 2})
	r2.RecordOwner(ownerRecord{ChatID: 1, MsgID: 2, Owner: &telego.User{ID: 1, IsBot: true}})
	r2.RecordOwner(ownerRecord{ChatID: 1, MsgID: 0, Owner: aliceUser})
	if _, ok := r2.index.Lookup(msgKey{chatID: 1, msgID: 2}); ok {
		t.Fatal("nil and bot users must not be indexed")
	}
	if _, ok := r2.index.Lookup(msgKey{chatID: 1, msgID: 0}); ok {
		t.Fatal("a message id of 0 must not be indexed")
	}
}

func TestRepReactorUnindexedBotMessageStillSkipped(t *testing.T) {
	r, _, store, cleanup := newRepReactorForTest(t)
	defer cleanup()
	_ = r.Handle(nil, reactionFor(bobUser, 999, emojiLike))
	if got := reactionWallet(t, store, aliceUser.ID); got.Available != 10 {
		t.Fatalf("unindexed message must not change balance, got %+v", got)
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
	bot := &telego.User{ID: 999, IsBot: true, Username: "somebot"}
	if err := r.Handle(nil, reactionFor(bot, 11, emojiLike)); err != nil {
		t.Fatalf("Handle bot: %v", err)
	}
	if got := reactionWallet(t, store, aliceUser.ID); got.Available != 10 {
		t.Fatalf("anonymous/bot reactions must not change balance, got %+v", got)
	}
}

func TestRepReactorSkipsUnknownMessage(t *testing.T) {
	r, _, store, cleanup := newRepReactorForTest(t)
	defer cleanup()
	_ = r.Handle(nil, reactionFor(bobUser, 13, emojiLike))
	if got := reactionWallet(t, store, aliceUser.ID); got.Available != 10 {
		t.Fatalf("unattributed reaction must not change balance, got %+v", got)
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
	if got := reactionWallet(t, store, bobUser.ID); got.Available != 10 {
		t.Fatalf("reaction on bot message must not debit reactor, got %+v", got)
	}
}

func TestRepReactorRemovalDoesNotReverseOrResettle(t *testing.T) {
	r, _, store, cleanup := newRepReactorForTest(t)
	defer cleanup()
	r.RecordMessage(&telego.Message{
		MessageID: 17,
		Chat:      telego.Chat{ID: -1001234567890},
		From:      aliceUser,
	})
	_ = r.Handle(nil, reactionFor(bobUser, 17, emojiLike))
	removed := telego.MessageReactionUpdated{
		Chat:        telego.Chat{ID: -1001234567890},
		MessageID:   17,
		User:        bobUser,
		OldReaction: []telego.ReactionType{&telego.ReactionTypeEmoji{Emoji: emojiLike}},
	}
	if err := r.Handle(nil, removed); err != nil {
		t.Fatalf("Handle removal: %v", err)
	}
	// Re-adding the same reaction is the same source event: it must not
	// settle twice.
	_ = r.Handle(nil, reactionFor(bobUser, 17, emojiLike))
	alice := reactionWallet(t, store, aliceUser.ID)
	if alice.Available != 13 || alice.Score != 3 {
		t.Fatalf("removal/re-add changed the wallet: %+v", alice)
	}
}

func TestRepReactorPairLimitStopsAlternatingPluses(t *testing.T) {
	r, _, store, cleanup := newRepReactorForTest(t)
	defer cleanup()
	r.RecordMessage(&telego.Message{
		MessageID: 31,
		Chat:      telego.Chat{ID: -1001234567890},
		From:      aliceUser,
	})
	r.RecordMessage(&telego.Message{
		MessageID: 32,
		Chat:      telego.Chat{ID: -1001234567890},
		From:      aliceUser,
	})
	// Bob likes two different Alice messages the same day: the second is
	// the same unordered pair and must not settle.
	_ = r.Handle(nil, reactionFor(bobUser, 31, emojiLike))
	_ = r.Handle(nil, reactionFor(bobUser, 32, emojiLike))
	alice := reactionWallet(t, store, aliceUser.ID)
	bob := reactionWallet(t, store, bobUser.ID)
	if alice.Available != 13 || alice.Score != 3 {
		t.Fatalf("pair limit failed on target: %+v", alice)
	}
	if bob.Available != 9 {
		t.Fatalf("pair limit failed on actor: %+v", bob)
	}
}

func TestRepReactorSenderLimitShared(t *testing.T) {
	r, _, store, cleanup := newRepReactorForTest(t)
	defer cleanup()
	authors := []*telego.User{
		{ID: 201, Username: "a1", FirstName: "A1"},
		{ID: 202, Username: "a2", FirstName: "A2"},
		{ID: 203, Username: "a3", FirstName: "A3"},
		{ID: 204, Username: "a4", FirstName: "A4"},
	}
	for i, author := range authors {
		r.RecordMessage(&telego.Message{
			MessageID: 40 + i,
			Chat:      telego.Chat{ID: -1001234567890},
			From:      author,
		})
	}
	for i := range 3 {
		_ = r.Handle(nil, reactionFor(bobUser, 40+i, emojiLike))
	}
	_ = r.Handle(nil, reactionFor(bobUser, 43, emojiLike))
	if got := reactionWallet(t, store, 204); got.Available != 10 || got.Score != 0 {
		t.Fatalf("fourth reaction must be dropped by the sender limit: %+v", got)
	}
}

func TestRepReactorAdminTargetGetsSameThree(t *testing.T) {
	r, _, store, cleanup := newRepReactorForTest(t)
	defer cleanup()
	r.RecordMessage(&telego.Message{
		MessageID: 19,
		Chat:      telego.Chat{ID: -1001234567890},
		From:      aliceUser, // alice is an administrator
	})
	_ = r.Handle(nil, reactionFor(bobUser, 19, emojiLike))
	alice := reactionWallet(t, store, aliceUser.ID)
	if alice.Available != 13 || alice.Score != 3 {
		t.Fatalf("administrator target payout = %+v, want 13/3", alice)
	}
}

func TestRepReactorFailsClosedWithoutMembership(t *testing.T) {
	r, sender, store, cleanup := newRepReactorForTest(t)
	defer cleanup()
	sender.DefaultMember = &telego.ChatMemberLeft{Status: "left"}
	r.RecordMessage(&telego.Message{
		MessageID: 23,
		Chat:      telego.Chat{ID: -1001234567890},
		From:      aliceUser,
	})
	_ = r.Handle(nil, reactionFor(bobUser, 23, emojiLike))
	if got := reactionWallet(t, store, aliceUser.ID); got.Available != 10 || got.Score != 0 {
		t.Fatalf("unverified membership must not settle: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// repBatcher
// ---------------------------------------------------------------------------

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

func praiseEvent(actor, target, line string) repEvent {
	return repEvent{
		kind: reputation.KindPraise, actor: actor, target: target, line: line,
		actorWalletDelta: -1, targetWalletDelta: 3, targetScoreDelta: 3,
		actorAvailable: 10, targetAvailable: 13, targetScore: 3,
	}
}

func TestRepBatcherFlushesCombinedMessage(t *testing.T) {
	sender := &stubRepSender{}
	r := newRepBatcher(sender, testLogger())
	r.window = 30 * time.Millisecond

	r.Add(-100, praiseEvent("carol", "alice", "Первый плюс."))
	r.Add(-100, repEvent{
		kind: reputation.KindRoast, actor: "dave", target: "bob", line: "Минус по адресу.",
		actorWalletDelta: -1, targetWalletDelta: -1, targetScoreDelta: -1,
		actorAvailable: 5, targetAvailable: 2, targetScore: 0,
	})
	texts := waitForSends(t, sender, 1)

	if !strings.Contains(texts[0], "Первый плюс.") || !strings.Contains(texts[0], "+3</b> к репе alice") {
		t.Fatalf("combined message missing praise entry, got %q", texts[0])
	}
	if !strings.Contains(texts[0], "Минус по адресу.") || !strings.Contains(texts[0], "Доступно: dave \u2014 5, bob \u2014 2") {
		t.Fatalf("combined message missing roast entry, got %q", texts[0])
	}
}

func TestRepBatcherSplitsWindows(t *testing.T) {
	sender := &stubRepSender{}
	r := newRepBatcher(sender, testLogger())
	r.window = 30 * time.Millisecond

	r.Add(-100, praiseEvent("carol", "alice", "Первый."))
	texts := waitForSends(t, sender, 1)
	if !strings.Contains(texts[0], "alice") {
		t.Fatalf("first window message wrong: %q", texts[0])
	}

	time.Sleep(60 * time.Millisecond)
	r.Add(-100, praiseEvent("carol", "carol", "Второй."))
	texts = waitForSends(t, sender, 2)
	if !strings.Contains(texts[1], "carol") || !strings.Contains(texts[1], "Второй.") {
		t.Fatalf("second window message wrong: %q", texts[1])
	}
}

func TestRepBatcherSeparatesChats(t *testing.T) {
	sender := &stubRepSender{}
	r := newRepBatcher(sender, testLogger())
	r.window = 30 * time.Millisecond

	r.Add(-100, praiseEvent("carol", "alice", "Первый."))
	r.Add(-200, praiseEvent("dave", "bob", "Второй."))
	texts := waitForSends(t, sender, 2)
	if len(texts) != 2 {
		t.Fatalf("two chats must produce two messages, got %d: %v", len(texts), texts)
	}
}

// ---------------------------------------------------------------------------
// Integration: fanout path
// ---------------------------------------------------------------------------

func TestRepReactorHandleBatchesIntoFanout(t *testing.T) {
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
	// Two different actors, so the sender limit does not interfere.
	_ = r.Handle(nil, reactionFor(carolUser, 21, emojiLike))
	_ = r.Handle(nil, reactionFor(daveUser, 22, emojiLike))

	texts := waitForSends(t, sender, 1)
	if !strings.Contains(texts[0], "alice") || !strings.Contains(texts[0], "bob") {
		t.Fatalf("batcher must combine both reactions, got %q", texts[0])
	}
}

// ---------------------------------------------------------------------------
// What was liked, and where to tap
// ---------------------------------------------------------------------------

func TestRepQuoteTextCollapsesWhitespace(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"empty", "", ""},
		{"whitespace only", "   \n\t ", ""},
		{"collapses runs", "  лови\n\n\n  этот\tвидео  ", "лови этот видео"},
		{"keeps markup for the composer to escape", `<b>a & b</b>`, `<b>a & b</b>`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := repQuoteText(c.in); got != c.want {
				t.Fatalf("repQuoteText(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestRepQuoteTextTruncatesOnRuneBoundary(t *testing.T) {
	long := strings.Repeat("ё", repQuoteMaxRunes+30)
	got := repQuoteText(long)
	if !utf8.ValidString(got) {
		t.Fatalf("truncation broke UTF-8: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != repQuoteMaxRunes+1 {
		t.Fatalf("quote has %d runes, want %d (+ ellipsis)", n, repQuoteMaxRunes+1)
	}
	if !strings.HasSuffix(got, "\u2026") {
		t.Fatalf("truncated quote must end with an ellipsis: %q", got)
	}
	exact := strings.Repeat("я", repQuoteMaxRunes)
	if got := repQuoteText(exact); got != exact {
		t.Fatalf("quote at the limit must not be touched, got %d runes", utf8.RuneCountInString(got))
	}
}

func TestRepQuoteForPrefersTextAndLabelsMedia(t *testing.T) {
	cases := []struct {
		name string
		msg  *telego.Message
		want string
	}{
		{"text", &telego.Message{Text: "привет"}, "привет"},
		{"caption", &telego.Message{Caption: "подпись"}, "подпись"},
		{"caption beats media label", &telego.Message{Video: &telego.Video{}, Caption: "смотри"}, "смотри"},
		{"bare photo", &telego.Message{Photo: []telego.PhotoSize{{}}}, "фото"},
		{"bare video", &telego.Message{Video: &telego.Video{}}, "видео"},
		{"bare video note", &telego.Message{VideoNote: &telego.VideoNote{}}, "кружок"},
		{"bare sticker", &telego.Message{Sticker: &telego.Sticker{}}, "стикер"},
		{"bare dice", &telego.Message{Dice: &telego.Dice{}}, "кубик"},
		{"bare location", &telego.Message{Location: &telego.Location{}}, "геолокация"},
		{"bare contact", &telego.Message{Contact: &telego.Contact{}}, "контакт"},
		{"bare game", &telego.Message{Game: &telego.Game{}}, "игра"},
		{"nothing at all", &telego.Message{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := repQuoteFor(c.msg); got != c.want {
				t.Fatalf("repQuoteFor = %q, want %q", got, c.want)
			}
		})
	}
}

func TestComposeRepEntryLinksTheQuote(t *testing.T) {
	text := composeRepMessage([]repEvent{{
		kind: reputation.KindPraise, actor: "carol", target: "alice", line: "Плюс по адресу.",
		actorWalletDelta: -1, targetWalletDelta: 3, targetScoreDelta: 3,
		actorAvailable: 10, targetAvailable: 13, targetScore: 3,
		quote: "починил деплой",
		link:  "https://t.me/c/1234567890/42",
	}})

	want := "<a href=\"https://t.me/c/1234567890/42\">\u00abпочинил деплой\u00bb</a>"
	if !strings.Contains(text, want) {
		t.Fatalf("quote must be the permalink's link text:\n%s", text)
	}
	if !strings.Contains(text, "Плюс по адресу.") {
		t.Fatalf("public line lost:\n%s", text)
	}
	if !strings.Contains(text, "+3</b> к репе alice") || !strings.Contains(text, "Счёт: alice \u2014 3") {
		t.Fatalf("receipt lost:\n%s", text)
	}
	if strings.Index(text, "\u00ab") > strings.Index(text, "Счёт:") {
		t.Fatalf("quote must sit above the wallet report:\n%s", text)
	}
}

func TestComposeRepEntryEscapesAndDefusesQuote(t *testing.T) {
	text := composeRepMessage([]repEvent{{
		kind: reputation.KindRoast, actor: "carol", target: "alice", line: "Минус по адресу.",
		actorWalletDelta: -1, targetWalletDelta: -1, targetScoreDelta: -1,
		quote: `<b>@dave</b> & co`,
	}})

	if strings.Contains(text, "<b>@dave") || strings.Contains(text, "<b>a & b</b>") {
		t.Fatalf("markup from the quoted message must be escaped:\n%s", text)
	}
	if !strings.Contains(text, "&lt;b&gt;") || !strings.Contains(text, "&amp; co") {
		t.Fatalf("quote must be HTML-escaped:\n%s", text)
	}
	if !strings.Contains(text, "@\u2060dave") {
		t.Fatalf("mention in the quote must be defused:\n%s", text)
	}
}

func TestComposeRepEntryFallsBackToNameLink(t *testing.T) {
	text := composeRepMessage([]repEvent{{
		kind: reputation.KindRoast, actor: "carol", target: "bob", line: "Минус по адресу.",
		actorWalletDelta: -1, targetWalletDelta: -1, targetScoreDelta: -1,
		link: "https://t.me/c/1234567890/7",
	}})
	if !strings.Contains(text, `<a href="https://t.me/c/1234567890/7">bob</a>`) {
		t.Fatalf("target name must carry the link when there is no quote:\n%s", text)
	}
	if strings.Contains(text, "\u00ab") {
		t.Fatalf("no quote line expected:\n%s", text)
	}

	plain := composeRepMessage([]repEvent{{
		kind: reputation.KindPraise, actor: "carol", target: "bob", line: "Плюс по адресу.",
		actorWalletDelta: -1, targetWalletDelta: 3, targetScoreDelta: 3,
	}})
	if strings.Contains(plain, "<a href") {
		t.Fatalf("no link expected without one:\n%s", plain)
	}
}

func TestComposeRepMessagesSplitsOnBudget(t *testing.T) {
	events := make([]repEvent, 0, 40)
	for range 40 {
		events = append(events, repEvent{
			kind: reputation.KindPraise, actor: "carol", target: "alice", line: praiseGeneric[0],
			actorWalletDelta: -1, targetWalletDelta: 3, targetScoreDelta: 3,
			actorAvailable: 1, targetAvailable: 2, targetScore: 3,
			quote: strings.Repeat("я", repQuoteMaxRunes),
			link:  "https://t.me/c/1234567890/42",
		})
	}

	if a, b := composeRepMessage(events), composeRepMessage(events); a != b {
		t.Fatal("composeRepMessage must be deterministic for events that carry their line")
	}

	msgs := composeRepMessages(events)
	if len(msgs) < 2 {
		t.Fatalf("a 40-event batch must split, got %d message(s)", len(msgs))
	}
	total := 0
	for _, m := range msgs {
		if len(m) > repMessageMaxChars {
			t.Fatalf("chunk of %d bytes equals or exceeds the %d-byte budget", len(m), repMessageMaxChars)
		}
		if u := utf16Units(m); u > 4096 {
			t.Fatalf("chunk of %d UTF-16 units exceeds Telegram's 4096 limit", u)
		}
		total += strings.Count(m, "Счёт:")
	}
	if total != len(events) {
		t.Fatalf("splitting dropped entries: %d of %d survived", total, len(events))
	}

	if got := composeRepMessages(events[:1]); len(got) != 1 {
		t.Fatalf("a single event must stay one message, got %d", len(got))
	}
	if got := composeRepMessages(nil); got != nil {
		t.Fatalf("no events must compose nothing, got %v", got)
	}
}

func utf16Units(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
			continue
		}
		n++
	}
	return n
}

func TestSentMessagesOf(t *testing.T) {
	if got := sentMessageOf(nil); got != (sentMessage{}) {
		t.Fatalf("sentMessageOf(nil) = %+v, want the zero value", got)
	}
	ms := []telego.Message{
		{MessageID: 7},
		{MessageID: 8, MessageThreadID: 17},
	}
	got := sentMessagesOf(ms)
	want := []sentMessage{{ID: 7}, {ID: 8, ThreadID: 17}}
	if len(got) != len(want) {
		t.Fatalf("sentMessagesOf = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sentMessagesOf[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if got := sentMessagesOf(nil); len(got) != 0 {
		t.Fatalf("no messages must map to no records, got %+v", got)
	}
}

func TestRepBatcherDisablesLinkPreview(t *testing.T) {
	sender := &stubRepSender{}
	b := newRepBatcher(sender, testLogger())
	b.window = 20 * time.Millisecond

	event := praiseEvent("carol", "alice", "Плюс по адресу.")
	event.quote = "починил деплой"
	event.link = "https://t.me/c/1234567890/42"
	b.Add(-100, event)
	waitForSends(t, sender, 1)

	sender.mu.Lock()
	defer sender.mu.Unlock()
	p := sender.Sent[0]
	if p.LinkPreviewOptions == nil || !p.LinkPreviewOptions.IsDisabled {
		t.Fatalf("the permalink must not expand into a web preview, got %+v", p.LinkPreviewOptions)
	}
	if p.ParseMode != telego.ModeHTML {
		t.Fatalf("rep report must stay HTML, got %q", p.ParseMode)
	}
}

func TestRepReactorReportsQuoteAndPermalink(t *testing.T) {
	r, sender, _, cleanup := newRepReactorForTest(t)
	defer cleanup()
	r.batch.window = 30 * time.Millisecond

	r.RecordMessage(&telego.Message{
		MessageID: 42,
		Chat:      telego.Chat{ID: -1001234567890},
		From:      aliceUser,
		Text:      "починил деплой на проде",
	})
	_ = r.Handle(nil, reactionFor(bobUser, 42, emojiLike))

	texts := waitForSends(t, sender, 1)
	if !strings.Contains(texts[0], "https://t.me/c/1234567890/42") {
		t.Fatalf("entry must link the reacted message:\n%s", texts[0])
	}
	if !strings.Contains(texts[0], "починил деплой на проде") {
		t.Fatalf("entry must quote the reacted message:\n%s", texts[0])
	}
}

func TestRepReactorTopicPermalinkCarriesThread(t *testing.T) {
	r, sender, _, cleanup := newRepReactorForTest(t)
	defer cleanup()
	r.batch.window = 30 * time.Millisecond

	r.RecordMessage(&telego.Message{
		MessageID:       42,
		MessageThreadID: 17,
		Chat:            telego.Chat{ID: -1001234567890},
		From:            aliceUser,
		Text:            "в топике",
	})
	_ = r.Handle(nil, reactionFor(bobUser, 42, emojiLike))

	texts := waitForSends(t, sender, 1)
	if !strings.Contains(texts[0], "https://t.me/c/1234567890/17/42") {
		t.Fatalf("forum topic link must carry the topic id:\n%s", texts[0])
	}
}

func TestRepReactorOwnerRepostCarriesQuote(t *testing.T) {
	r, sender, _, cleanup := newRepReactorForTest(t)
	defer cleanup()
	r.batch.window = 30 * time.Millisecond

	r.RecordOwner(ownerRecord{
		ChatID: -1001234567890, MsgID: 21, Owner: aliceUser,
		Text: "👤 Иван писал(а): ловите видос",
	})
	_ = r.Handle(nil, reactionFor(bobUser, 21, emojiLike))

	texts := waitForSends(t, sender, 1)
	if !strings.Contains(texts[0], "ловите видос") {
		t.Fatalf("repost quote lost:\n%s", texts[0])
	}
	if !strings.Contains(texts[0], "https://t.me/c/1234567890/21") {
		t.Fatalf("repost permalink lost:\n%s", texts[0])
	}
}
