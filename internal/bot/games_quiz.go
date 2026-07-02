package bot

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/games/quiz"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
)

// GamesCallbackPrefix namespaces game-owned callback_data so the
// pending-action dispatcher (which owns "v1:") never tries to look up
// a quiz callback as a pending action. The router predicate filters on
// this prefix; everything else falls through to the dispatcher.
const GamesCallbackPrefix = "g1:"

// quizCallbackVerb is the segment that follows the namespace for quiz
// callbacks. Example callback_data: "g1:q:7:2".
const quizCallbackVerb = "q"

// quizSender is the narrow telego surface QuizHandler needs.
type quizSender interface {
	SendMessage(ctx context.Context, params *telego.SendMessageParams) (*telego.Message, error)
	EditMessageText(ctx context.Context, params *telego.EditMessageTextParams) (*telego.Message, error)
	AnswerCallbackQuery(ctx context.Context, params *telego.AnswerCallbackQueryParams) error
}

// QuizHandler implements /quiz and /quiz top. Quiz state during play
// lives in *quiz.ActiveQuizzes; the leaderboard lives in quiz.Store.
type QuizHandler struct {
	active *quiz.ActiveQuizzes
	repo   quiz.Store
	bot    quizSender
	log    *slog.Logger

	// rand lets tests inject a deterministic source. nil falls back to
	// a fresh time-seeded *rand.Rand on every dispatch (acceptable for
	// the small number of quiz invocations per chat).
	rand *rand.Rand
}

func NewQuizHandler(active *quiz.ActiveQuizzes, repo quiz.Store, bot quizSender, log *slog.Logger) *QuizHandler {
	if log == nil {
		log = slog.Default()
	}
	return &QuizHandler{
		active: active,
		repo:   repo,
		bot:    bot,
		log:    log,
	}
}

// HandleQuiz routes /quiz and /quiz top.
func (h *QuizHandler) HandleQuiz(_ *th.Context, msg telego.Message) error {
	if msg.From == nil || msg.From.IsBot {
		return nil
	}
	parts := strings.Fields(msg.Text)
	if len(parts) >= 2 && strings.EqualFold(parts[1], "top") {
		return h.handleTop(msg)
	}
	return h.handleStart(msg)
}

func (h *QuizHandler) handleStart(msg telego.Message) error {
	r := h.rand
	if r == nil {
		r = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	snippetIdx := quiz.PickRandom(r)
	options, correctIdx, err := quiz.BuildOptions(snippetIdx, r)
	if err != nil {
		h.log.Warn("quiz BuildOptions failed", "error", err, "snippet_idx", snippetIdx)
		return h.replyText(msg.Chat.ID, msg.MessageID, "Не удалось подготовить квиз. Попробуйте позже.")
	}
	snippet, _ := quiz.GetSnippet(snippetIdx)

	body := renderQuizBody(snippet.Code)
	kb := renderQuizKeyboard(snippetIdx, options)

	sent, err := h.bot.SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID:      telego.ChatID{ID: msg.Chat.ID},
		Text:        body,
		ParseMode:   telego.ModeHTML,
		ReplyMarkup: kb,
	})
	if err != nil {
		h.log.Warn("quiz send failed", "error", err, "chat_id", msg.Chat.ID)
		_ = h.replyText(msg.Chat.ID, msg.MessageID, "Не удалось запустить квиз. Попробуйте позже.")
		return nil
	}

	h.active.Register(&quiz.ActiveQuiz{
		MessageID:  int64(sent.MessageID),
		AbsChatID:  storage.AbsChatID(msg.Chat.ID),
		SnippetIdx: snippetIdx,
		CorrectIdx: correctIdx,
		Options:    options,
		StartedAt:  time.Now().UTC(),
	})
	return nil
}

func (h *QuizHandler) handleTop(msg telego.Message) error {
	absChatID := storage.AbsChatID(msg.Chat.ID)
	entries, err := h.repo.TopByChat(context.Background(), absChatID, 5)
	if err != nil {
		h.log.Warn("quiz TopByChat failed", "error", err, "chat_id", absChatID)
		return h.replyText(msg.Chat.ID, msg.MessageID, "Не удалось получить топ. Попробуйте позже.")
	}
	body := renderQuizTop(entries)
	_, err = h.bot.SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: msg.Chat.ID},
		Text:      body,
		ParseMode: telego.ModeHTML,
		ReplyParameters: &telego.ReplyParameters{
			MessageID: msg.MessageID,
		},
	})
	return err
}

// QuizCallbackPredicate returns a th.Predicate that matches every
// callback_query whose data starts with the games namespace. Used by
// routes.go to register the quiz callback BEFORE the pending-action
// dispatcher.
func QuizCallbackPredicate() th.Predicate {
	return func(_ context.Context, update telego.Update) bool {
		cb := update.CallbackQuery
		if cb == nil {
			return false
		}
		return strings.HasPrefix(cb.Data, GamesCallbackPrefix)
	}
}

// HandleCallback is the th.CallbackQueryHandler for quiz buttons. It
// validates the data, marks the quiz solved on first correct tap,
// updates the leaderboard, and edits the original message to show the
// outcome. Subsequent taps after the quiz is solved get an "уже
// разгадано" toast; wrong taps before solving get "не верно".
func (h *QuizHandler) HandleCallback(ctx *th.Context, query telego.CallbackQuery) error {
	resp := h.dispatchCallback(ctx.Context(), query)
	_ = h.bot.AnswerCallbackQuery(ctx.Context(), &telego.AnswerCallbackQueryParams{
		CallbackQueryID: query.ID,
		Text:            resp.Toast,
		ShowAlert:       resp.Alert,
	})
	if resp.EditedText == "" || query.Message == nil {
		return nil
	}
	chatID := query.Message.GetChat().ID
	messageID := query.Message.GetMessageID()
	if _, err := h.bot.EditMessageText(ctx.Context(), &telego.EditMessageTextParams{
		ChatID:      telego.ChatID{ID: chatID},
		MessageID:   messageID,
		Text:        resp.EditedText,
		ParseMode:   telego.ModeHTML,
		ReplyMarkup: emptyKeyboard(),
	}); err != nil {
		h.log.Warn("quiz EditMessageText failed", "error", err)
	}
	return nil
}

// quizCallbackResponse describes the side effects to apply after a
// callback. EditedText empty means "leave the message as is".
type quizCallbackResponse struct {
	Toast      string
	Alert      bool
	EditedText string
}

func (h *QuizHandler) dispatchCallback(ctx context.Context, query telego.CallbackQuery) quizCallbackResponse {
	snippetIdx, chosenIdx, ok := parseQuizCallback(query.Data)
	if !ok {
		return quizCallbackResponse{Toast: "Эта кнопка устарела."}
	}
	if query.Message == nil {
		return quizCallbackResponse{Toast: "Сообщение недоступно."}
	}
	msgID := int64(query.Message.GetMessageID())

	q := h.active.Get(msgID)
	if q == nil {
		return quizCallbackResponse{Toast: "Квиз больше не активен."}
	}
	if q.SnippetIdx != snippetIdx {
		// callback_data references a different snippet than the active
		// quiz on this message - shouldn't happen, but stay defensive
		return quizCallbackResponse{Toast: "Кнопка не от этого квиза."}
	}
	if q.WinnerID != 0 {
		return quizCallbackResponse{Toast: "Уже разгадано - " + q.WinnerName + "."}
	}

	if chosenIdx < 0 || chosenIdx >= len(q.Options) {
		return quizCallbackResponse{Toast: "Неверная кнопка."}
	}

	user := query.From
	display := shared.UserDisplay(user.Username, user.FirstName)
	if display == "" {
		display = fmt.Sprintf("user %d", user.ID)
	}

	if chosenIdx != q.CorrectIdx {
		// Wrong: silent no-op for the message; toast for the user.
		chosen := q.Options[chosenIdx]
		return quizCallbackResponse{Toast: "Не угадал - это не " + chosen.Title() + "."}
	}

	// Correct: race to be first.
	if !h.active.MarkSolved(msgID, user.ID, display) {
		return quizCallbackResponse{Toast: "Уже разгадано."}
	}

	snippet, err := quiz.GetSnippet(q.SnippetIdx)
	if err != nil {
		// Should never happen: the snippet existed at /quiz time.
		h.log.Warn("quiz callback: snippet missing on resolve", "snippet_idx", q.SnippetIdx)
		return quizCallbackResponse{Toast: "Сниппет пропал."}
	}

	if err := h.repo.IncrementCorrect(ctx, quiz.Entry{
		AbsChatID:    q.AbsChatID,
		UserID:       user.ID,
		Username:     user.Username,
		FirstName:    user.FirstName,
		LastPlayedAt: time.Now().UTC(),
	}); err != nil {
		h.log.Warn("quiz IncrementCorrect failed", "error", err, "user_id", user.ID, "chat_id", q.AbsChatID)
		// Still announce the win even if leaderboard write failed.
	}

	h.active.Forget(msgID)

	body := fmt.Sprintf(
		"%s\n\n✅ Первым угадал %s - это %s.",
		renderQuizBody(snippet.Code),
		display,
		snippet.Answer.Title(),
	)
	return quizCallbackResponse{
		Toast:      "Верно!",
		EditedText: body,
	}
}

// renderQuizBody is the message body shown when the quiz is posted and
// reused (with an appended winner line) when the quiz is solved.
func renderQuizBody(code string) string {
	return "\U0001F9E9 <b>Угадай язык по сниппету</b>\n<pre>" + html.EscapeString(code) + "</pre>"
}

// renderQuizKeyboard builds the four-button inline keyboard. Buttons
// carry callback_data of the form "g1:q:<snippetIdx>:<chosenIdx>".
// Total length is bounded: snippetIdx <= 3 digits in a realistic
// snippet pool, chosenIdx is 1 digit, prefix is 5 chars - well under
// Telegram's 64-byte limit.
func renderQuizKeyboard(snippetIdx int, options []quiz.Lang) *telego.InlineKeyboardMarkup {
	row := make([]telego.InlineKeyboardButton, 0, 4)
	for i, l := range options {
		row = append(row, telego.InlineKeyboardButton{
			Text:         l.Title(),
			CallbackData: fmt.Sprintf("%s%s:%d:%d", GamesCallbackPrefix, quizCallbackVerb, snippetIdx, i),
		})
	}
	// Two rows of two buttons each render better on mobile than a
	// single row of four.
	return &telego.InlineKeyboardMarkup{
		InlineKeyboard: [][]telego.InlineKeyboardButton{
			row[:2], row[2:],
		},
	}
}

// renderQuizTop formats the leaderboard reply for /quiz top.
func renderQuizTop(entries []quiz.Entry) string {
	if len(entries) == 0 {
		return "\U0001F9E9 <b>Топ квиза</b>\n\nПока никто не угадал ни одного сниппета."
	}
	var b strings.Builder
	b.WriteString("\U0001F9E9 <b>Топ квиза</b>\n\n")
	for i, e := range entries {
		display := shared.UserDisplay(e.Username, e.FirstName)
		if display == "" {
			display = fmt.Sprintf("user %d", e.UserID)
		}
		fmt.Fprintf(&b, "%d. %s - %d\n", i+1, display, e.CorrectCount)
	}
	return b.String()
}

// parseQuizCallback parses "g1:q:<snippetIdx>:<chosenIdx>" into its two
// numeric components. Any other shape returns ok=false.
func parseQuizCallback(data string) (snippetIdx, chosenIdx int, ok bool) {
	if !strings.HasPrefix(data, GamesCallbackPrefix+quizCallbackVerb+":") {
		return 0, 0, false
	}
	rest := data[len(GamesCallbackPrefix+quizCallbackVerb+":"):]
	colon := strings.IndexByte(rest, ':')
	if colon <= 0 || colon == len(rest)-1 {
		return 0, 0, false
	}
	a, err1 := strconv.Atoi(rest[:colon])
	b, err2 := strconv.Atoi(rest[colon+1:])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return a, b, true
}

// makeQuizCallback is exported only for tests in this package; the
// keyboard builder uses the same format inline.
func makeQuizCallback(snippetIdx, chosenIdx int) string {
	return fmt.Sprintf("%s%s:%d:%d", GamesCallbackPrefix, quizCallbackVerb, snippetIdx, chosenIdx)
}

func (h *QuizHandler) replyText(chatID int64, replyTo int, body string) error {
	_, err := h.bot.SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: chatID},
		Text:   body,
		ReplyParameters: &telego.ReplyParameters{
			MessageID: replyTo,
		},
	})
	return err
}
