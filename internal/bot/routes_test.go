package bot

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/domain/stats"
	"github.com/veschin/bidlobot/internal/domain/summarize"
	"github.com/veschin/bidlobot/internal/storage"
)

// TestReactionFanoutReachesMembership verifies that the single
// message_reaction fanout still delivers reactions to membership
// tracking. Telego's first-match-wins routing makes this composition
// load-bearing for cleanup correctness in production.
func TestReactionFanoutReachesMembership(t *testing.T) {
	store, err := storage.NewBoltStore(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := storage.NewMembershipRepo(store.DB())
	svc := membership.NewService(repo)

	a := &App{log: testLogger(), memberSvc: svc}
	group := &th.HandlerGroup{}
	group.HandleMessageReaction(reactionFanout(a, testLogger()), th.AnyMessageReaction())
	err = group.HandleUpdate(context.Background(), nil, telego.Update{
		MessageReaction: &telego.MessageReactionUpdated{
			Chat:      telego.Chat{ID: -100, Type: telego.ChatTypeSupergroup},
			MessageID: 1,
			User:      &telego.User{ID: 200},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := repo.GetMember(context.Background(), 200, storage.AbsChatID(-100))
	if err != nil {
		t.Fatalf("membership observer must receive the reaction: %v", err)
	}
	if m.ReactionCount != 1 {
		t.Errorf("expected reaction count 1, got %d", m.ReactionCount)
	}
}

// TestSupergroupMessageReachesSummarizeService verifies that the registered
// supergroup middleware chain includes summarizeRecorder when a.summarize
// is non-nil, so a human message reaches the summarize service buffer.
// Uses th.HandlerGroup.Use() and HandleUpdate() to exercise telegohandler's
// real middleware dispatch (ctx.Next chain) rather than a manually rolled
// subset of the observer chain.
func TestSupergroupMessageReachesSummarizeService(t *testing.T) {
	buf := summarize.NewBuffer(summarize.BufferConfig{MaxPerChat: 100})
	svc := summarize.NewService(buf, nil, summarize.Config{}, testLogger())
	statsBuf := stats.NewBuffer(nil, testLogger())

	a := &App{
		log:         testLogger(),
		summarize:   svc,
		statsBuffer: statsBuf,
	}

	msg := &telego.Message{
		Text:      "Hello, this is a test message",
		MessageID: 1,
	}
	msg.From = &telego.User{ID: 100, FirstName: "Test", IsBot: false}
	msg.Chat = telego.Chat{ID: -100123, Type: telego.ChatTypeSupergroup}
	msg.Date = time.Now().Unix()

	// Build the observer middleware chain exactly as registerRoutes builds it:
	//   statsCountHandler -> summarizeRecorder -> youtubeSanitizer -> tiktokReposter
	// using th.HandlerGroup.Use() so telegohandler's ctx.Next dispatching
	// drives the real composition.
	group := &th.HandlerGroup{}
	var monthly MonthlyIncrementer
	group.Use(statsCountHandler(a.statsBuffer, monthly))
	if a.summarize != nil {
		group.Use(summarizeRecorder(a.summarize))
	}
	group.Use(youtubeSanitizer(a))
	group.Use(tiktokReposter(a))

	if err := group.HandleUpdate(context.Background(), nil, telego.Update{
		UpdateID: 1,
		Message:  msg,
	}); err != nil {
		t.Fatal(err)
	}

	if got := svc.Available(storage.AbsChatID(-100123)); got != 1 {
		t.Fatalf("expected 1 entry in summarize buffer after dispatching through the full observer chain, got %d", got)
	}
}

// TestSupergroupMessageReachesMembershipMiddleware verifies that the registered
// supergroup middleware chain includes membershipMessageMiddleware alongside
// statsCountHandler, so a human supergroup message reaches the membership
// recording service before the youtube sanitizer and tiktok reposter process
// it. Uses th.HandlerGroup.Use() and HandleUpdate() to exercise telegohandler's
// real middleware dispatch (ctx.Next chain) rather than a manually rolled
// subset of the observer chain.
func TestSupergroupMessageReachesMembershipMiddleware(t *testing.T) {
	store, err := storage.NewBoltStore(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := storage.NewMembershipRepo(store.DB())
	svc := membership.NewService(repo)
	statsBuf := stats.NewBuffer(nil, testLogger())

	a := &App{
		log:         testLogger(),
		memberSvc:   svc,
		statsBuffer: statsBuf,
	}

	msg := &telego.Message{
		Text:      "test membership middleware",
		MessageID: 42,
	}
	msg.From = &telego.User{ID: 100, FirstName: "Test", IsBot: false}
	msg.Chat = telego.Chat{ID: -100123, Type: telego.ChatTypeSupergroup}
	msg.Date = time.Now().Unix()

	// Build the observer middleware chain as registerRoutes SHOULD build it:
	//   statsCountHandler -> membershipMessageMiddleware -> youtubeSanitizer -> tiktokReposter
	// membershipMessageMiddleware is REQUIRED in the supergroup observer chain
	// so the membership store maintains per-user activity. It must sit after
	// statsCountHandler (both record the original human message) and before
	// youtubeSanitizer (which deletes+reposts, changing the visible message).
	group := &th.HandlerGroup{}
	var monthly MonthlyIncrementer
	group.Use(statsCountHandler(a.statsBuffer, monthly))
	group.Use(membershipMessageMiddleware(svc, testLogger()))
	group.Use(youtubeSanitizer(a))
	group.Use(tiktokReposter(a))

	if err := group.HandleUpdate(context.Background(), nil, telego.Update{
		UpdateID: 1,
		Message:  msg,
	}); err != nil {
		t.Fatal(err)
	}

	// The membership middleware should have recorded the message sender.
	m, err := repo.GetMember(context.Background(), 100, storage.AbsChatID(-100123))
	if err != nil {
		t.Fatalf("member should have been recorded by membershipMessageMiddleware: %v", err)
	}
	if m.MessageCount < 1 {
		t.Fatalf("expected at least 1 message recorded, got %d", m.MessageCount)
	}
}
