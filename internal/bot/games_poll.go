package bot

import (
	"context"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

// Telegram native-poll limits (Bot API). Question 1-300 chars, each
// option 1-100 chars. The task constrains the option count to 2-10
// (the API itself allows up to 12, but we keep the tighter bound).
const (
	pollMaxQuestionLen = 300
	pollMaxOptionLen   = 100
	pollMinOptions     = 2
	pollMaxOptions     = 10
)

// pollSender is the narrow telego surface the /poll handler needs.
// GamesSender (the shared interface in games.go) does NOT expose
// SendPoll and that file must not be edited, so the handler declares
// its own interface. Production must wire the rate-limited tgclient
// wrapper, which exposes SendPoll + SendMessage and therefore satisfies
// this. Tests substitute a recording stub.
type pollSender interface {
	SendPoll(ctx context.Context, params *telego.SendPollParams) (*telego.Message, error)
	SendMessage(ctx context.Context, params *telego.SendMessageParams) (*telego.Message, error)
}

// PollHandler wires "/poll" to a native Telegram poll. Stateless: no
// DB, the poll itself lives in Telegram. Two syntaxes:
//
//	/poll Вопрос | Вариант1 | Вариант2 | ...
//	/poll quiz Вопрос | *ВерныйВариант | Вариант2 | ...
//
// In quiz mode exactly one option must be prefixed with "*" to mark the
// correct answer; the "*" is stripped before sending. Both regular and
// quiz polls are anonymous.
type PollHandler struct {
	bot pollSender
	log *slog.Logger
}

func NewPollHandler(bot pollSender, log *slog.Logger) *PollHandler {
	if log == nil {
		log = slog.Default()
	}
	return &PollHandler{bot: bot, log: log}
}

// parsedPoll is the validated result of interpreting a /poll command.
type parsedPoll struct {
	question  string
	options   []string
	isQuiz    bool
	correctID int // index into options; meaningful only when isQuiz
}

// HandlePoll handles "/poll ..." and "/poll quiz ...". On any syntax or
// limit error it replies (reply-to the command) with a Russian usage
// hint and does not send a poll. On a Telegram send failure it logs and
// posts a short retry hint.
func (h *PollHandler) HandlePoll(_ *th.Context, msg telego.Message) error {
	if msg.From == nil || msg.From.IsBot {
		return nil
	}

	body := commandArgs(msg.Text)
	parsed, errMsg := parsePollCommand(body)
	if errMsg != "" {
		return h.reply(msg, errMsg)
	}

	options := make([]telego.InputPollOption, 0, len(parsed.options))
	for _, o := range parsed.options {
		options = append(options, telego.InputPollOption{Text: o})
	}

	anonymous := true
	params := &telego.SendPollParams{
		ChatID:      telego.ChatID{ID: msg.Chat.ID},
		Question:    parsed.question,
		Options:     options,
		IsAnonymous: &anonymous,
		ReplyParameters: &telego.ReplyParameters{
			MessageID: msg.MessageID,
		},
	}
	if parsed.isQuiz {
		params.Type = "quiz"
		// telego v1.8 exposes the plural CorrectOptionIDs ([]int); a
		// single-answer quiz passes exactly one index.
		params.CorrectOptionIDs = []int{parsed.correctID}
	}

	if _, err := h.bot.SendPoll(context.Background(), params); err != nil {
		h.log.Warn("sendPoll failed", "error", err, "chat_id", msg.Chat.ID, "quiz", parsed.isQuiz)
		return h.reply(msg, "Не удалось создать опрос. Попробуйте позже.")
	}
	return nil
}

// parsePollCommand validates and splits the command argument string.
// Returns a non-empty errMsg (a ready Russian hint) when the input is
// invalid; parsed is meaningful only when errMsg == "".
//
// Rules:
//   - leading "quiz" token (case-insensitive) selects quiz mode;
//   - the remainder is split on "|" into question + options, each
//     trimmed;
//   - 2..10 non-empty options, question non-empty;
//   - question <= 300 chars, each option <= 100 chars (rune-counted,
//     matching how Telegram counts);
//   - quiz mode: exactly one option prefixed with "*" marks the
//     correct answer; the marker is stripped and the stripped text
//     must still be non-empty.
func parsePollCommand(arg string) (parsed parsedPoll, errMsg string) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return parsedPoll{}, pollUsage(false)
	}

	isQuiz := false
	// Detect a leading "quiz" token: it must be followed by whitespace
	// (so a question literally starting with the word "quiz" without a
	// following space is not misread - edge, but handled).
	if len(arg) >= 4 && strings.EqualFold(arg[:4], "quiz") {
		rest := arg[4:]
		if rest == "" || rest[0] == ' ' || rest[0] == '\t' || rest[0] == '\n' {
			isQuiz = true
			arg = strings.TrimSpace(rest)
		}
	}
	if arg == "" {
		return parsedPoll{}, pollUsage(isQuiz)
	}

	rawParts := strings.Split(arg, "|")
	parts := make([]string, 0, len(rawParts))
	for _, p := range rawParts {
		parts = append(parts, strings.TrimSpace(p))
	}

	if len(parts) < 1+pollMinOptions {
		return parsedPoll{}, pollUsage(isQuiz)
	}

	question := parts[0]
	rawOptions := parts[1:]

	if question == "" {
		return parsedPoll{}, "Вопрос не может быть пустым.\n\n" + pollUsage(isQuiz)
	}
	if utf8.RuneCountInString(question) > pollMaxQuestionLen {
		return parsedPoll{}, "Вопрос слишком длинный (максимум 300 символов).\n\n" + pollUsage(isQuiz)
	}

	options := make([]string, 0, len(rawOptions))
	correctID := -1
	for i, o := range rawOptions {
		text := o
		if isQuiz && strings.HasPrefix(o, "*") {
			if correctID != -1 {
				return parsedPoll{}, "Отметьте звёздочкой только один верный вариант.\n\n" + pollUsage(true)
			}
			correctID = i
			text = strings.TrimSpace(strings.TrimPrefix(o, "*"))
		}
		if text == "" {
			return parsedPoll{}, "Варианты не могут быть пустыми.\n\n" + pollUsage(isQuiz)
		}
		if utf8.RuneCountInString(text) > pollMaxOptionLen {
			return parsedPoll{}, "Вариант слишком длинный (максимум 100 символов).\n\n" + pollUsage(isQuiz)
		}
		options = append(options, text)
	}

	if len(options) < pollMinOptions || len(options) > pollMaxOptions {
		return parsedPoll{}, pollUsage(isQuiz)
	}

	if isQuiz && correctID == -1 {
		return parsedPoll{}, "В режиме quiz отметьте верный вариант звёздочкой, например: *Go\n\n" + pollUsage(true)
	}

	return parsedPoll{
		question:  question,
		options:   options,
		isQuiz:    isQuiz,
		correctID: correctID,
	}, ""
}

// pollUsage returns the Russian syntax hint for the requested mode.
func pollUsage(quiz bool) string {
	if quiz {
		return "Опрос-квиз: <code>/poll quiz Вопрос | *ВерныйВариант | Вариант2 | ...</code>\n" +
			"Отметьте один верный вариант звёздочкой. От 2 до 10 вариантов."
	}
	return "Опрос: <code>/poll Вопрос | Вариант1 | Вариант2 | ...</code>\n" +
		"От 2 до 10 вариантов. Для квиза: <code>/poll quiz Вопрос | *Верный | ...</code>"
}

func (h *PollHandler) reply(msg telego.Message, body string) error {
	_, err := h.bot.SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: msg.Chat.ID},
		Text:      body,
		ParseMode: telego.ModeHTML,
		ReplyParameters: &telego.ReplyParameters{
			MessageID: msg.MessageID,
		},
	})
	if err != nil {
		h.log.Warn("poll reply failed", "error", err, "chat_id", msg.Chat.ID)
	}
	return err
}
