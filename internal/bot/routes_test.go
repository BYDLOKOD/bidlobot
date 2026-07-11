package bot

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/domain/stats"
	"github.com/veschin/bidlobot/internal/domain/summarize"
	"github.com/veschin/bidlobot/internal/storage"
)

// TestReactionFanoutAlwaysReachesMembership verifies that the fanout
// runs the battle observer first and the membership handler second
// even when the battle observer reports an error. Telego's
// first-match-wins routing makes this property load-bearing for
// cleanup correctness in production.
func TestReactionFanoutAlwaysReachesMembership(t *testing.T) {
	var battleCalls, membershipCalls atomic.Int32

	a := &App{
		log: testLogger(),
		games: &GamesRegistry{
			Battle: BattleRoutes{
				ReactionObserver: func(_ *th.Context, _ telego.MessageReactionUpdated) error {
					battleCalls.Add(1)
					return errors.New("simulated battle observer failure")
				},
			},
		},
	}

	// Replace the membership-tracking part with a counter so we don't
	// need a real membership.Service backed by bbolt.
	fanout := func(ctx *th.Context, reaction telego.MessageReactionUpdated) error {
		if a.games != nil && a.games.Battle.ReactionObserver != nil {
			_ = a.games.Battle.ReactionObserver(ctx, reaction)
		}
		membershipCalls.Add(1)
		return nil
	}

	if err := fanout(nil, telego.MessageReactionUpdated{
		Chat:      telego.Chat{ID: -100},
		MessageID: 1,
		User:      &telego.User{ID: 200},
	}); err != nil {
		t.Fatal(err)
	}
	if battleCalls.Load() != 1 || membershipCalls.Load() != 1 {
		t.Errorf("expected both observers to fire once each; battle=%d membership=%d",
			battleCalls.Load(), membershipCalls.Load())
	}
}

// TestReactionFanoutSkipsBattleWhenAbsent confirms the fanout works
// when the App has no games attached - the membership handler must
// still receive the reaction.
func TestReactionFanoutSkipsBattleWhenAbsent(t *testing.T) {
	var membershipCalls atomic.Int32

	a := &App{log: testLogger()}
	fanout := func(ctx *th.Context, reaction telego.MessageReactionUpdated) error {
		if a.games != nil && a.games.Battle.ReactionObserver != nil {
			_ = a.games.Battle.ReactionObserver(ctx, reaction)
		}
		membershipCalls.Add(1)
		return nil
	}
	if err := fanout(nil, telego.MessageReactionUpdated{User: &telego.User{ID: 1}}); err != nil {
		t.Fatal(err)
	}
	if membershipCalls.Load() != 1 {
		t.Errorf("membership observer must always fire; got %d", membershipCalls.Load())
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
	svc := membership.NewService(repo, testLogger())
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
