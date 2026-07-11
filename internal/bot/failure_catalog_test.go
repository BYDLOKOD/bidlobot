package bot

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

// --- 19 approved public pure-failure literals (plan §1.2) ----------------
//
// This is the ONLY set of generic operational-failure strings the bot may
// reply with in public supergroups. Every string is owner-approved; no
// unapproved Russian literal may be returned for TikTok media failure,
// game start/send/provider failure, stale public callbacks, or public
// cooldown notices.

var approvedFailureStrings = []string{
	"Не, чота не хочу пока...",
	"Я попытался...",
	"Оно там развалилось...",
	"Я туда сходил... зря...",
	"Ну все, приплыли бля...",
	"Я устал уже на середине...",
	"Не сегодня...",
	"Я думал, это на минутку... не на минутку...",
	"Оно само...",
	"Я уже чота не хочу...",
	"Нет... спасибо...",
	"Да ну его нахуй...",
	"Я перепроверил... всё плохо...",
	"Оно мне не отвечает...",
	"Может потом...",
	"И так-то день был непростой...",
	"Лучше не спрашивай...",
	"Там какая-то возня...",
	"Я думал, проканает...",
}

func failureCatalogContains(s string) bool {
	for _, want := range approvedFailureStrings {
		if s == want || len(s) >= len(want) && s[:len(want)] == want {
			return true
		}
	}
	return false
}

// --- Contract specification: the catalog must exist and hold these 19 -----

func TestFailureCatalog_ApprovedStringsAreValid(t *testing.T) {
	if len(approvedFailureStrings) != 19 {
		t.Fatalf("approved catalog must contain exactly 19 strings, got %d", len(approvedFailureStrings))
	}
	seen := make(map[string]bool, 19)
	for i, s := range approvedFailureStrings {
		if s == "" {
			t.Fatalf("approvedFailureStrings[%d] is empty", i)
		}
		if seen[s] {
			t.Fatalf("duplicate approved string at index %d: %q", i, s)
		}
		seen[s] = true
	}
	t.Logf("contract: %d unique, non-empty approved failure strings", len(approvedFailureStrings))
}

// --- Selector injectability contract -------------------------------------

func TestFailureCatalog_SelectorIsDeterministic(t *testing.T) {
	// A production FailureCatalog must expose a deterministic or seeded
	// selector so tests can fix the output. This test proves the concept
	// via a test-only deterministic catalog. Once wired, a production
	// handler must accept the catalog as an injected dependency.
	sel := &sequentialSelector{index: 0}
	got := sel.Pick(approvedFailureStrings)
	if got != approvedFailureStrings[0] {
		t.Fatalf("sequentialSelector.Pick() = %q, want %q", got, approvedFailureStrings[0])
	}
}

// sequentialSelector returns items in order - proves deterministic
// selection without requiring a *rand.Rand.
type sequentialSelector struct {
	index int
	mu    sync.Mutex
}

func (s *sequentialSelector) Pick(pool []string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index
	s.index = (s.index + 1) % len(pool)
	return pool[i]
}

// --- Stub shared.TelegramAPI for cooldown notice tests -------------------

// recordMessageSender records every SendMessage call. Implements the full
// shared.TelegramAPI so NewApp accepts it as the rate-limited sender.
type recordMessageSender struct {
	mu       sync.Mutex
	Messages []*telego.SendMessageParams
}

func (s *recordMessageSender) SendMessage(_ context.Context, p *telego.SendMessageParams) (*telego.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = append(s.Messages, p)
	return &telego.Message{MessageID: 1000}, nil
}
func (s *recordMessageSender) EditMessageText(_ context.Context, _ *telego.EditMessageTextParams) (*telego.Message, error) {
	return nil, nil
}
func (s *recordMessageSender) SendAnimation(_ context.Context, _ *telego.SendAnimationParams) (*telego.Message, error) {
	return nil, nil
}
func (s *recordMessageSender) GetChatAdministrators(_ context.Context, _ *telego.GetChatAdministratorsParams) ([]telego.ChatMember, error) {
	return nil, nil
}
func (s *recordMessageSender) GetChatMember(_ context.Context, _ *telego.GetChatMemberParams) (telego.ChatMember, error) {
	return nil, nil
}
func (s *recordMessageSender) GetChat(_ context.Context, _ *telego.GetChatParams) (*telego.ChatFullInfo, error) {
	return nil, nil
}
func (s *recordMessageSender) RestrictChatMember(_ context.Context, _ *telego.RestrictChatMemberParams) error {
	return nil
}
func (s *recordMessageSender) BanChatMember(_ context.Context, _ *telego.BanChatMemberParams) error {
	return nil
}
func (s *recordMessageSender) UnbanChatMember(_ context.Context, _ *telego.UnbanChatMemberParams) error {
	return nil
}
func (s *recordMessageSender) DeleteMessage(_ context.Context, _ *telego.DeleteMessageParams) error {
	return nil
}
func (s *recordMessageSender) AnswerCallbackQuery(_ context.Context, _ *telego.AnswerCallbackQueryParams) error {
	return nil
}
func (s *recordMessageSender) GetMe(_ context.Context) (*telego.User, error) {
	return &telego.User{ID: 999, IsBot: true}, nil
}

// --- RED tests: production code must use FailureCatalog ------------------
//
// Every test below calls existing production code and asserts the output
// text is one of the 19 approved failure strings. The assertion fails
// because the production code still uses hardcoded literals - this is the
// intended RED signal proving the catalog seam is absent.

func TestTikTok_DownloadFail_MustUseFailureCatalog(t *testing.T) {
	snd := &recYTSender{}
	log := slog.New(slog.DiscardHandler)

	msg := &telego.Message{
		MessageID: 42,
		Chat:      telego.Chat{ID: -100123, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
	}
	// Pass a non-existent file path - os.Stat fails, triggering
	// sendDecline with msgTikTokDownloadFail.
	processTikTok(context.Background(), snd, log, msg,
		"https://www.tiktok.com/@user/video/123",
		filepath.Join(t.TempDir(), "nonexistent.mp4"))

	if len(snd.Messages) == 0 {
		t.Fatal("expected a decline message when video does not exist on disk")
	}
	if !failureCatalogContains(snd.Messages[0].Text) {
		t.Errorf("TikTok download-fail decline must be from FailureCatalog; got hardcoded %q",
			snd.Messages[0].Text)
	}
}

func TestTikTok_SizeLimit_MustUseFailureCatalog(t *testing.T) {
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "big.mp4")
	f, err := os.Create(videoPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxVideoSize + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	snd := &recYTSender{}
	log := slog.New(slog.DiscardHandler)

	msg := &telego.Message{
		MessageID: 42,
		Chat:      telego.Chat{ID: -100123, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
	}

	processTikTok(context.Background(), snd, log, msg,
		"https://www.tiktok.com/@user/video/123", videoPath)

	if len(snd.Messages) != 1 {
		t.Fatalf("expected 1 decline message, got %d", len(snd.Messages))
	}
	if !failureCatalogContains(snd.Messages[0].Text) {
		t.Errorf("TikTok size-limit decline must be from FailureCatalog; got hardcoded %q",
			snd.Messages[0].Text)
	}
}

func TestCooldown_Notice_MustUseFailureCatalog(t *testing.T) {
	snd := &recordMessageSender{}
	a := NewApp(nil, snd, testLogger(), nil, nil, nil, nil, nil, nil, nil)

	noop := func(_ *th.Context, _ telego.Message) error { return nil }
	gated := a.gateMsg("testcmd", time.Second, noop)

	thctx := (&th.Context{}).WithContext(context.Background())
	msg := telego.Message{
		MessageID: 1,
		Chat:      telego.Chat{ID: -100123, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
	}

	// First call: allowed, no notice.
	if err := gated(thctx, msg); err != nil {
		t.Fatalf("first gated call: %v", err)
	}
	// Second immediate call: blocked, must send a cooldown notice.
	if err := gated(thctx, msg); err != nil {
		t.Fatalf("second gated call: %v", err)
	}

	if len(snd.Messages) == 0 {
		t.Fatal("expected cooldown notice message after over-frequency call")
	}
	if !failureCatalogContains(snd.Messages[0].Text) {
		t.Errorf("cooldown notice text must be from FailureCatalog; got hardcoded %q",
			snd.Messages[0].Text)
	}
}

func TestStaleCallback_Answer_MustUseFailureCatalog(t *testing.T) {
	store := newFakePending()
	d := NewCallbackDispatcher(store, nil, nil, testLogger())

	resp := d.dispatch(context.Background(), telego.CallbackQuery{Data: "garbage"})

	if resp.AnswerText == "" {
		t.Fatal("unknown callback data must produce a toast answer")
	}
	if !failureCatalogContains(resp.AnswerText) {
		t.Errorf("stale callback answer must be from FailureCatalog; got hardcoded %q",
			resp.AnswerText)
	}
}

func TestDice_SendDiceError_MustUseFailureCatalog(t *testing.T) {
	h, bot, _ := newDiceTestHandler(6)
	bot.SendDiceErr = errors.New("rate limit")

	if err := h.HandleDice(nil, newDiceTestMessage("/dice")); err != nil {
		t.Fatal(err)
	}
	if len(bot.MessageCalls) == 0 {
		t.Fatalf("expected fallback reply when SendDice fails, got 0 messages")
	}
	if !failureCatalogContains(bot.MessageCalls[0].Text) {
		t.Errorf("dice provider failure reply must be from FailureCatalog; got hardcoded %q",
			bot.MessageCalls[0].Text)
	}
}
