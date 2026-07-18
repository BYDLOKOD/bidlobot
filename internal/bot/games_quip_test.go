package bot

import (
	"context"
	"errors"
	"math/rand"
	"strings"
	"sync"
	"testing"

	"github.com/mymmrac/telego"
)

// stubQuipSender records SendMessage params for assertions.
type stubQuipSender struct {
	mu sync.Mutex

	SendErr error

	Sent []*telego.SendMessageParams
}

func (s *stubQuipSender) SendMessage(_ context.Context, params *telego.SendMessageParams) (*telego.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Sent = append(s.Sent, params)
	if s.SendErr != nil {
		return nil, s.SendErr
	}
	return &telego.Message{MessageID: 2000 + len(s.Sent)}, nil
}

func newQuipTestHandler(seed int64) (*QuipHandler, *stubQuipSender) {
	bot := &stubQuipSender{}
	h := &QuipHandler{bot: bot, log: testLogger(), rand: rand.New(rand.NewSource(seed))}
	return h, bot
}

func newQuipMsg(text string) telego.Message {
	return telego.Message{
		MessageID: 7,
		Chat:      telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
		Text:      text,
	}
}

func quipMatchesAnyTemplate(body string, templates []string) bool {
	for _, tmpl := range templates {
		// Replace the single %s with a wildcard split: check the
		// non-target prefix/suffix of the template are present.
		idx := strings.Index(tmpl, "%s")
		prefix := tmpl[:idx]
		suffix := tmpl[idx+2:]
		if (prefix == "" || strings.Contains(body, prefix)) &&
			(suffix == "" || strings.Contains(body, suffix)) {
			return true
		}
	}
	return false
}

func TestRoastSelfWhenNoArg(t *testing.T) {
	h, bot := newQuipTestHandler(1)
	if err := h.HandleRoast(nil, newQuipMsg("/roast")); err != nil {
		t.Fatalf("HandleRoast: %v", err)
	}
	if len(bot.Sent) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(bot.Sent))
	}
	body := bot.Sent[0].Text
	if !strings.Contains(body, "alice") {
		t.Errorf("no-arg roast should target the caller @alice, got %q", body)
	}
	if !quipMatchesAnyTemplate(body, roastTemplates) {
		t.Errorf("body %q does not match any roast template", body)
	}
	if bot.Sent[0].ReplyParameters == nil || bot.Sent[0].ReplyParameters.MessageID != 7 {
		t.Error("reply must be reply-to the command")
	}
	if bot.Sent[0].ParseMode != telego.ModeHTML {
		t.Errorf("expected HTML parse mode, got %q", bot.Sent[0].ParseMode)
	}
}

func TestPraiseSelfWhenNoArg(t *testing.T) {
	h, bot := newQuipTestHandler(2)
	if err := h.HandlePraise(nil, newQuipMsg("/praise")); err != nil {
		t.Fatal(err)
	}
	body := bot.Sent[0].Text
	if !strings.Contains(body, "alice") {
		t.Errorf("no-arg praise should target caller, got %q", body)
	}
	if !quipMatchesAnyTemplate(body, praiseTemplates) {
		t.Errorf("body %q does not match any praise template", body)
	}
}

func TestRoastTargetsMentionedUser(t *testing.T) {
	h, bot := newQuipTestHandler(3)
	if err := h.HandleRoast(nil, newQuipMsg("/roast @bob")); err != nil {
		t.Fatal(err)
	}
	body := bot.Sent[0].Text
	if !strings.Contains(body, "bob") {
		t.Errorf("roast should target @bob, got %q", body)
	}
	if strings.Contains(body, "alice") {
		t.Errorf("roast should NOT target the caller when @bob given, got %q", body)
	}
}

func TestPraiseTargetMentionedUserWithoutAt(t *testing.T) {
	// A bare "bob" (no @) is treated as a handle and rendered INERT (no @).
	h, bot := newQuipTestHandler(4)
	if err := h.HandlePraise(nil, newQuipMsg("/praise bob")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bot.Sent[0].Text, "bob") {
		t.Errorf("expected @bob target, got %q", bot.Sent[0].Text)
	}
}

func TestQuipTargetTakesFirstTokenOnly(t *testing.T) {
	h, bot := newQuipTestHandler(5)
	if err := h.HandleRoast(nil, newQuipMsg("/roast @bob and his friends")); err != nil {
		t.Fatal(err)
	}
	body := bot.Sent[0].Text
	if !strings.Contains(body, "bob") {
		t.Errorf("expected @bob, got %q", body)
	}
	if strings.Contains(body, "friends") {
		t.Errorf("trailing words must be ignored, got %q", body)
	}
}

func TestQuipEscapesHTMLInHandle(t *testing.T) {
	h, bot := newQuipTestHandler(6)
	// A crafted argument must not inject HTML markup.
	if err := h.HandleRoast(nil, newQuipMsg("/roast @<b>x</b>")); err != nil {
		t.Fatal(err)
	}
	body := bot.Sent[0].Text
	if strings.Contains(body, "<b>") {
		t.Errorf("raw HTML must be escaped in handle, got %q", body)
	}
	if !strings.Contains(body, "&lt;b&gt;") {
		t.Errorf("expected escaped handle, got %q", body)
	}
}

func TestQuipFallsBackToFirstNameWhenNoUsername(t *testing.T) {
	h, bot := newQuipTestHandler(7)
	msg := newQuipMsg("/praise")
	msg.From = &telego.User{ID: 9, FirstName: "Боб"}
	if err := h.HandlePraise(nil, msg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bot.Sent[0].Text, "Боб") {
		t.Errorf("expected first-name fallback, got %q", bot.Sent[0].Text)
	}
}

func TestQuipDeterministicWithSeed(t *testing.T) {
	h1, bot1 := newQuipTestHandler(99)
	h2, bot2 := newQuipTestHandler(99)
	if err := h1.HandleRoast(nil, newQuipMsg("/roast")); err != nil {
		t.Fatal(err)
	}
	if err := h2.HandleRoast(nil, newQuipMsg("/roast @someoneelse")); err != nil {
		t.Fatal(err)
	}
	// Same seed -> same template index chosen (target differs but the
	// surrounding template text must be identical).
	b1 := strings.Replace(bot1.Sent[0].Text, "alice", "X", 1)
	b2 := strings.Replace(bot2.Sent[0].Text, "someoneelse", "X", 1)
	if b1 != b2 {
		t.Errorf("same seed must pick same template: %q vs %q", b1, b2)
	}
}

func TestQuipNoFromIgnored(t *testing.T) {
	h, bot := newQuipTestHandler(1)
	msg := newQuipMsg("/roast")
	msg.From = nil
	if err := h.HandleRoast(nil, msg); err != nil {
		t.Fatal(err)
	}
	if len(bot.Sent) != 0 {
		t.Errorf("message without From must be ignored, got %d", len(bot.Sent))
	}
}

func TestQuipBotSenderIgnored(t *testing.T) {
	h, bot := newQuipTestHandler(1)
	msg := newQuipMsg("/praise")
	msg.From = &telego.User{ID: 3, IsBot: true}
	if err := h.HandlePraise(nil, msg); err != nil {
		t.Fatal(err)
	}
	if len(bot.Sent) != 0 {
		t.Errorf("bot sender must be ignored, got %d", len(bot.Sent))
	}
}

func TestQuipSendErrorPropagates(t *testing.T) {
	h, bot := newQuipTestHandler(1)
	bot.SendErr = errors.New("network")
	if err := h.HandleRoast(nil, newQuipMsg("/roast")); err == nil {
		t.Fatal("send error should propagate")
	}
}

func TestQuipTemplatesCuratedCounts(t *testing.T) {
	if len(roastTemplates) < 35 {
		t.Errorf("roast pool too small for replayability: %d", len(roastTemplates))
	}
	if len(praiseTemplates) < 35 {
		t.Errorf("praise pool too small for replayability: %d", len(praiseTemplates))
	}
	all := append(append([]string{}, roastTemplates...), praiseTemplates...)
	for i, tmpl := range all {
		if strings.Count(tmpl, "%s") != 1 {
			t.Errorf("template %d must contain exactly one %%s placeholder: %q", i, tmpl)
		}
		// Voice guard: the selected sad-bot corpus closes every
		// one-liner with an unfinished thought marked by "...". A
		// template missing it is a regression.
		if !strings.Contains(tmpl, "...") {
			t.Errorf("template %d must contain \"...\" to keep the elliptical voice: %q", i, tmpl)
		}
	}
}

func TestResolveQuipTargetEmptyArgFallsBackToParticipant(t *testing.T) {
	msg := newQuipMsg("/roast")
	msg.From = &telego.User{ID: 1} // no username, no first name
	if got := resolveQuipTarget(msg); got != "участник" {
		t.Errorf("expected 'участник' fallback, got %q", got)
	}
}
