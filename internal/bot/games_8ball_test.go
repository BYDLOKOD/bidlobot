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

// stub8BallSender records SendMessage params so tests can assert what
// would have been sent without a live bot.
type stub8BallSender struct {
	mu sync.Mutex

	SendErr error

	Sent []*telego.SendMessageParams
}

func (s *stub8BallSender) SendMessage(_ context.Context, params *telego.SendMessageParams) (*telego.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Sent = append(s.Sent, params)
	if s.SendErr != nil {
		return nil, s.SendErr
	}
	return &telego.Message{MessageID: 1000 + len(s.Sent)}, nil
}

func newEightBallTestHandler(seed int64) (*EightBallHandler, *stub8BallSender) {
	bot := &stub8BallSender{}
	h := &EightBallHandler{bot: bot, log: testLogger(), rand: rand.New(rand.NewSource(seed))}
	return h, bot
}

func newEightBallMsg(text string) telego.Message {
	return telego.Message{
		MessageID: 1,
		Chat:      telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
		Text:      text,
	}
}

func TestEightBallReturnsCuratedAnswer(t *testing.T) {
	h, bot := newEightBallTestHandler(1)
	if err := h.HandleEightBall(nil, newEightBallMsg("/8ball Стоит ли катить в прод?")); err != nil {
		t.Fatalf("HandleEightBall: %v", err)
	}
	if len(bot.Sent) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(bot.Sent))
	}
	got := bot.Sent[0].Text
	matched := false
	for _, a := range eightBallAnswers {
		if strings.Contains(got, a) {
			matched = true
			break
		}
	}
	if !matched {
		t.Errorf("reply %q is not one of the curated answers", got)
	}
	if !strings.HasPrefix(got, "\U0001F3B1") {
		t.Errorf("reply should start with the 8-ball emoji, got %q", got)
	}
	if bot.Sent[0].ReplyParameters == nil || bot.Sent[0].ReplyParameters.MessageID != 1 {
		t.Errorf("reply must be reply-to the command message")
	}
	if bot.Sent[0].ParseMode != telego.ModeHTML {
		t.Errorf("expected HTML parse mode, got %q", bot.Sent[0].ParseMode)
	}
}

func TestEightBallDeterministicWithSeed(t *testing.T) {
	h1, bot1 := newEightBallTestHandler(42)
	h2, bot2 := newEightBallTestHandler(42)
	if err := h1.HandleEightBall(nil, newEightBallMsg("/8ball вопрос один")); err != nil {
		t.Fatal(err)
	}
	if err := h2.HandleEightBall(nil, newEightBallMsg("/8ball совсем другой вопрос")); err != nil {
		t.Fatal(err)
	}
	if bot1.Sent[0].Text != bot2.Sent[0].Text {
		t.Errorf("same seed must yield same answer: %q vs %q", bot1.Sent[0].Text, bot2.Sent[0].Text)
	}
}

func TestEightBallEmptyQuestionGetsHint(t *testing.T) {
	for _, text := range []string{"/8ball", "/8ball   ", "/8ball\t"} {
		h, bot := newEightBallTestHandler(1)
		if err := h.HandleEightBall(nil, newEightBallMsg(text)); err != nil {
			t.Fatalf("text %q: %v", text, err)
		}
		if len(bot.Sent) != 1 {
			t.Fatalf("text %q: expected 1 hint reply, got %d", text, len(bot.Sent))
		}
		if !strings.Contains(bot.Sent[0].Text, "/8ball") {
			t.Errorf("text %q: expected usage hint, got %q", text, bot.Sent[0].Text)
		}
		// The hint must not be a curated verdict.
		for _, a := range eightBallAnswers {
			if strings.Contains(bot.Sent[0].Text, a) {
				t.Errorf("text %q: empty question must not produce a verdict", text)
			}
		}
	}
}

func TestEightBallNoFromIgnored(t *testing.T) {
	h, bot := newEightBallTestHandler(1)
	msg := newEightBallMsg("/8ball вопрос")
	msg.From = nil
	if err := h.HandleEightBall(nil, msg); err != nil {
		t.Fatal(err)
	}
	if len(bot.Sent) != 0 {
		t.Errorf("message without From must be ignored, got %d sends", len(bot.Sent))
	}
}

func TestEightBallBotSenderIgnored(t *testing.T) {
	h, bot := newEightBallTestHandler(1)
	msg := newEightBallMsg("/8ball вопрос")
	msg.From = &telego.User{ID: 5, IsBot: true}
	if err := h.HandleEightBall(nil, msg); err != nil {
		t.Fatal(err)
	}
	if len(bot.Sent) != 0 {
		t.Errorf("bot sender must be ignored, got %d sends", len(bot.Sent))
	}
}

func TestEightBallSendErrorPropagates(t *testing.T) {
	h, bot := newEightBallTestHandler(1)
	bot.SendErr = errors.New("rate limit")
	err := h.HandleEightBall(nil, newEightBallMsg("/8ball вопрос"))
	if err == nil {
		t.Fatal("send error should propagate from handler")
	}
}

func TestEightBallAnswersAllNonEmptyAndCounted(t *testing.T) {
	if len(eightBallAnswers) != 20 {
		t.Errorf("expected 20 curated answers, got %d", len(eightBallAnswers))
	}
	for i, a := range eightBallAnswers {
		if strings.TrimSpace(a) == "" {
			t.Errorf("answer %d is empty", i)
		}
	}
}

func TestCommandArgsExtraction(t *testing.T) {
	cases := map[string]string{
		"/8ball вопрос тут":  "вопрос тут",
		"/8ball":             "",
		"/8ball   ":          "",
		"  /cmd  hello  ":    "hello",
		"/cmd\tтаб аргумент": "таб аргумент",
	}
	for in, want := range cases {
		if got := commandArgs(in); got != want {
			t.Errorf("commandArgs(%q) = %q, want %q", in, got, want)
		}
	}
}
