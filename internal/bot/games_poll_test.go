package bot

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/mymmrac/telego"
)

// stubPollSender records SendPoll and SendMessage params.
type stubPollSender struct {
	mu sync.Mutex

	SendPollErr error
	SendMsgErr  error

	Polls []*telego.SendPollParams
	Sent  []*telego.SendMessageParams
}

func (s *stubPollSender) SendPoll(_ context.Context, params *telego.SendPollParams) (*telego.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Polls = append(s.Polls, params)
	if s.SendPollErr != nil {
		return nil, s.SendPollErr
	}
	return &telego.Message{MessageID: 3000 + len(s.Polls)}, nil
}

func (s *stubPollSender) SendMessage(_ context.Context, params *telego.SendMessageParams) (*telego.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Sent = append(s.Sent, params)
	if s.SendMsgErr != nil {
		return nil, s.SendMsgErr
	}
	return &telego.Message{MessageID: 4000 + len(s.Sent)}, nil
}

func newPollTestHandler() (*PollHandler, *stubPollSender) {
	bot := &stubPollSender{}
	return &PollHandler{bot: bot, log: testLogger()}, bot
}

func newPollMsg(text string) telego.Message {
	return telego.Message{
		MessageID: 11,
		Chat:      telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
		Text:      text,
	}
}

func TestPollRegularValid(t *testing.T) {
	h, bot := newPollTestHandler()
	if err := h.HandlePoll(nil, newPollMsg("/poll Любимый язык? | Go | Rust | Python")); err != nil {
		t.Fatalf("HandlePoll: %v", err)
	}
	if len(bot.Polls) != 1 {
		t.Fatalf("expected 1 poll, got %d (msgs=%d)", len(bot.Polls), len(bot.Sent))
	}
	p := bot.Polls[0]
	if p.Question != "Любимый язык?" {
		t.Errorf("question = %q", p.Question)
	}
	if len(p.Options) != 3 {
		t.Fatalf("expected 3 options, got %d", len(p.Options))
	}
	want := []string{"Go", "Rust", "Python"}
	for i, o := range p.Options {
		if o.Text != want[i] {
			t.Errorf("option %d = %q, want %q", i, o.Text, want[i])
		}
	}
	if p.Type != "" {
		t.Errorf("regular poll must not set Type, got %q", p.Type)
	}
	if p.IsAnonymous == nil || !*p.IsAnonymous {
		t.Errorf("regular poll must be anonymous")
	}
	if p.ReplyParameters == nil || p.ReplyParameters.MessageID != 11 {
		t.Errorf("poll must reply-to the command")
	}
	if len(p.CorrectOptionIDs) != 0 {
		t.Errorf("regular poll must not set CorrectOptionIDs, got %v", p.CorrectOptionIDs)
	}
}

func TestPollTrimsWhitespaceAroundParts(t *testing.T) {
	h, bot := newPollTestHandler()
	if err := h.HandlePoll(nil, newPollMsg("/poll   Вопрос  |  A  |   B  ")); err != nil {
		t.Fatal(err)
	}
	if len(bot.Polls) != 1 {
		t.Fatalf("expected 1 poll, got %d", len(bot.Polls))
	}
	p := bot.Polls[0]
	if p.Question != "Вопрос" || p.Options[0].Text != "A" || p.Options[1].Text != "B" {
		t.Errorf("whitespace not trimmed: q=%q opts=%+v", p.Question, p.Options)
	}
}

func TestPollQuizValid(t *testing.T) {
	h, bot := newPollTestHandler()
	if err := h.HandlePoll(nil, newPollMsg("/poll quiz Чем пишут бэкенд? | *Go | HTML | CSS")); err != nil {
		t.Fatal(err)
	}
	if len(bot.Polls) != 1 {
		t.Fatalf("expected 1 quiz poll, got %d (msgs=%d: %v)", len(bot.Polls), len(bot.Sent), sentTexts(bot))
	}
	p := bot.Polls[0]
	if p.Type != "quiz" {
		t.Errorf("expected Type=quiz, got %q", p.Type)
	}
	if len(p.CorrectOptionIDs) != 1 || p.CorrectOptionIDs[0] != 0 {
		t.Errorf("expected CorrectOptionIDs=[0], got %v", p.CorrectOptionIDs)
	}
	if p.Options[0].Text != "Go" {
		t.Errorf("the '*' marker must be stripped from the correct option, got %q", p.Options[0].Text)
	}
	if p.IsAnonymous == nil || !*p.IsAnonymous {
		t.Errorf("quiz poll must be anonymous by default")
	}
}

func TestPollQuizCorrectOptionNotFirst(t *testing.T) {
	h, bot := newPollTestHandler()
	if err := h.HandlePoll(nil, newPollMsg("/poll quiz 2+2? | три | *четыре | пять")); err != nil {
		t.Fatal(err)
	}
	if len(bot.Polls) != 1 {
		t.Fatalf("expected 1 poll, got %d", len(bot.Polls))
	}
	if got := bot.Polls[0].CorrectOptionIDs; len(got) != 1 || got[0] != 1 {
		t.Errorf("expected correct index 1, got %v", got)
	}
	if bot.Polls[0].Options[1].Text != "четыре" {
		t.Errorf("marker not stripped, got %q", bot.Polls[0].Options[1].Text)
	}
}

func TestPollQuizCaseInsensitiveKeyword(t *testing.T) {
	h, bot := newPollTestHandler()
	if err := h.HandlePoll(nil, newPollMsg("/poll QUIZ Q? | *A | B")); err != nil {
		t.Fatal(err)
	}
	if len(bot.Polls) != 1 || bot.Polls[0].Type != "quiz" {
		t.Errorf("QUIZ keyword should be case-insensitive; polls=%d", len(bot.Polls))
	}
}

func TestPollQuizMissingCorrectMarker(t *testing.T) {
	h, bot := newPollTestHandler()
	if err := h.HandlePoll(nil, newPollMsg("/poll quiz Q? | A | B")); err != nil {
		t.Fatal(err)
	}
	if len(bot.Polls) != 0 {
		t.Fatal("quiz without a marked option must not send a poll")
	}
	if len(bot.Sent) != 1 || !strings.Contains(bot.Sent[0].Text, "звёздочк") {
		t.Errorf("expected hint about marking the correct option, got %v", sentTexts(bot))
	}
}

func TestPollQuizMultipleCorrectMarkers(t *testing.T) {
	h, bot := newPollTestHandler()
	if err := h.HandlePoll(nil, newPollMsg("/poll quiz Q? | *A | *B")); err != nil {
		t.Fatal(err)
	}
	if len(bot.Polls) != 0 {
		t.Fatal("two marked options must be rejected")
	}
	if len(bot.Sent) != 1 || !strings.Contains(bot.Sent[0].Text, "только один") {
		t.Errorf("expected single-correct hint, got %v", sentTexts(bot))
	}
}

func TestPollTooFewOptions(t *testing.T) {
	h, bot := newPollTestHandler()
	for _, text := range []string{"/poll Вопрос", "/poll Вопрос | один"} {
		bot.Polls = nil
		bot.Sent = nil
		if err := h.HandlePoll(nil, newPollMsg(text)); err != nil {
			t.Fatalf("%q: %v", text, err)
		}
		if len(bot.Polls) != 0 {
			t.Errorf("%q: must not send poll with <2 options", text)
		}
		if len(bot.Sent) != 1 {
			t.Errorf("%q: expected usage hint, got %d", text, len(bot.Sent))
		}
	}
}

func TestPollTooManyOptions(t *testing.T) {
	h, bot := newPollTestHandler()
	opts := make([]string, 11)
	for i := range opts {
		opts[i] = "o"
	}
	text := "/poll Q? | " + strings.Join(opts, " | ")
	if err := h.HandlePoll(nil, newPollMsg(text)); err != nil {
		t.Fatal(err)
	}
	if len(bot.Polls) != 0 {
		t.Error("11 options must be rejected (max 10)")
	}
	if len(bot.Sent) != 1 {
		t.Errorf("expected usage hint, got %d", len(bot.Sent))
	}
}

func TestPollExactlyTenOptionsAccepted(t *testing.T) {
	h, bot := newPollTestHandler()
	opts := make([]string, 10)
	for i := range opts {
		opts[i] = string(rune('A' + i))
	}
	text := "/poll Q? | " + strings.Join(opts, " | ")
	if err := h.HandlePoll(nil, newPollMsg(text)); err != nil {
		t.Fatal(err)
	}
	if len(bot.Polls) != 1 {
		t.Fatalf("10 options must be accepted, got polls=%d msgs=%v", len(bot.Polls), sentTexts(bot))
	}
	if len(bot.Polls[0].Options) != 10 {
		t.Errorf("expected 10 options, got %d", len(bot.Polls[0].Options))
	}
}

func TestPollEmptyQuestion(t *testing.T) {
	h, bot := newPollTestHandler()
	if err := h.HandlePoll(nil, newPollMsg("/poll  | A | B")); err != nil {
		t.Fatal(err)
	}
	if len(bot.Polls) != 0 {
		t.Error("empty question must be rejected")
	}
	if len(bot.Sent) != 1 || !strings.Contains(bot.Sent[0].Text, "Вопрос") {
		t.Errorf("expected empty-question hint, got %v", sentTexts(bot))
	}
}

func TestPollEmptyOption(t *testing.T) {
	h, bot := newPollTestHandler()
	if err := h.HandlePoll(nil, newPollMsg("/poll Q? | A |  | C")); err != nil {
		t.Fatal(err)
	}
	if len(bot.Polls) != 0 {
		t.Error("empty option must be rejected")
	}
	if len(bot.Sent) != 1 || !strings.Contains(bot.Sent[0].Text, "пустыми") {
		t.Errorf("expected empty-option hint, got %v", sentTexts(bot))
	}
}

func TestPollNoArgs(t *testing.T) {
	h, bot := newPollTestHandler()
	if err := h.HandlePoll(nil, newPollMsg("/poll")); err != nil {
		t.Fatal(err)
	}
	if len(bot.Polls) != 0 {
		t.Error("bare /poll must not send")
	}
	if len(bot.Sent) != 1 || !strings.Contains(bot.Sent[0].Text, "/poll") {
		t.Errorf("expected usage hint, got %v", sentTexts(bot))
	}
}

func TestPollQuestionTooLong(t *testing.T) {
	h, bot := newPollTestHandler()
	long := strings.Repeat("я", pollMaxQuestionLen+1)
	if err := h.HandlePoll(nil, newPollMsg("/poll "+long+" | A | B")); err != nil {
		t.Fatal(err)
	}
	if len(bot.Polls) != 0 {
		t.Error("over-length question must be rejected")
	}
	if len(bot.Sent) != 1 || !strings.Contains(bot.Sent[0].Text, "длинн") {
		t.Errorf("expected length hint, got %v", sentTexts(bot))
	}
}

func TestPollQuestionExactly300Accepted(t *testing.T) {
	h, bot := newPollTestHandler()
	q := strings.Repeat("я", pollMaxQuestionLen) // rune count = 300
	if err := h.HandlePoll(nil, newPollMsg("/poll "+q+" | A | B")); err != nil {
		t.Fatal(err)
	}
	if len(bot.Polls) != 1 {
		t.Errorf("300-rune question must be accepted, got %v", sentTexts(bot))
	}
}

func TestPollOptionTooLong(t *testing.T) {
	h, bot := newPollTestHandler()
	long := strings.Repeat("я", pollMaxOptionLen+1)
	if err := h.HandlePoll(nil, newPollMsg("/poll Q? | A | "+long)); err != nil {
		t.Fatal(err)
	}
	if len(bot.Polls) != 0 {
		t.Error("over-length option must be rejected")
	}
	if len(bot.Sent) != 1 || !strings.Contains(bot.Sent[0].Text, "длинн") {
		t.Errorf("expected length hint, got %v", sentTexts(bot))
	}
}

func TestPollSendPollErrorReplies(t *testing.T) {
	h, bot := newPollTestHandler()
	bot.SendPollErr = errors.New("rate limit")
	if err := h.HandlePoll(nil, newPollMsg("/poll Q? | A | B")); err != nil {
		t.Fatalf("handler should swallow send error after replying: %v", err)
	}
	if len(bot.Sent) != 1 || !strings.Contains(bot.Sent[0].Text, "Не удалось") {
		t.Errorf("expected failure reply, got %v", sentTexts(bot))
	}
}

func TestPollNoFromIgnored(t *testing.T) {
	h, bot := newPollTestHandler()
	msg := newPollMsg("/poll Q? | A | B")
	msg.From = nil
	if err := h.HandlePoll(nil, msg); err != nil {
		t.Fatal(err)
	}
	if len(bot.Polls) != 0 || len(bot.Sent) != 0 {
		t.Errorf("message without From must be ignored entirely")
	}
}

func TestPollBotSenderIgnored(t *testing.T) {
	h, bot := newPollTestHandler()
	msg := newPollMsg("/poll Q? | A | B")
	msg.From = &telego.User{ID: 1, IsBot: true}
	if err := h.HandlePoll(nil, msg); err != nil {
		t.Fatal(err)
	}
	if len(bot.Polls) != 0 || len(bot.Sent) != 0 {
		t.Errorf("bot sender must be ignored entirely")
	}
}

func TestPollErrorReplyIsReplyTo(t *testing.T) {
	h, bot := newPollTestHandler()
	if err := h.HandlePoll(nil, newPollMsg("/poll")); err != nil {
		t.Fatal(err)
	}
	if len(bot.Sent) != 1 {
		t.Fatalf("expected hint, got %d", len(bot.Sent))
	}
	if bot.Sent[0].ReplyParameters == nil || bot.Sent[0].ReplyParameters.MessageID != 11 {
		t.Error("hint must be reply-to the command")
	}
	if bot.Sent[0].ParseMode != telego.ModeHTML {
		t.Errorf("hint must use HTML parse mode, got %q", bot.Sent[0].ParseMode)
	}
}

func TestParsePollCommandQuizKeywordNotMisreadAsQuestion(t *testing.T) {
	// "quizzes" starts with "quiz" but is not the quiz keyword (no
	// following whitespace) - it must be treated as the question.
	parsed, errMsg := parsePollCommand("quizzes | A | B")
	if errMsg != "" {
		t.Fatalf("unexpected error: %q", errMsg)
	}
	if parsed.isQuiz {
		t.Error("'quizzes' must not enable quiz mode")
	}
	if parsed.question != "quizzes" {
		t.Errorf("question = %q, want 'quizzes'", parsed.question)
	}
}

// sentTexts is a tiny helper for failure messages.
func sentTexts(b *stubPollSender) []string {
	out := make([]string, 0, len(b.Sent))
	for _, s := range b.Sent {
		out = append(out, s.Text)
	}
	return out
}
