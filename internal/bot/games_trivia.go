package bot

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/games/quiz"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
)

// triviaCallbackVerb is the segment after the games namespace for
// trivia callbacks. callback_data shape: "g1:t:<triviaIdx>:<chosenIdx>".
//
// It is DISTINCT from quizCallbackVerb ("q"). Both live under the shared
// GamesCallbackPrefix ("g1:"), and telego routes a callback to the
// first-matched handler in registration order. TriviaCallbackPredicate
// matches only "g1:t:", so registering the trivia callback BEFORE the
// quiz callback (whose predicate is the broad "g1:" prefix) sends
// "g1:t:" here and leaves "g1:q:" for quiz. See the wiring report.
const triviaCallbackVerb = "t"

// triviaSender is the narrow telego surface TriviaHandler needs (mirrors
// quizSender).
type triviaSender interface {
	SendMessage(ctx context.Context, params *telego.SendMessageParams) (*telego.Message, error)
	EditMessageText(ctx context.Context, params *telego.EditMessageTextParams) (*telego.Message, error)
	AnswerCallbackQuery(ctx context.Context, params *telego.AnswerCallbackQueryParams) error
}

// activeTrivia is one in-flight trivia question. Memory-only, exactly
// like quiz.ActiveQuiz: a restart cancels in-flight questions (buttons
// keep working but award no credit) - acceptable for a question that
// resolves in seconds.
type activeTrivia struct {
	MessageID  int64
	AbsChatID  int64
	TriviaIdx  int
	CorrectIdx int
	Options    []string
	StartedAt  time.Time
	WinnerID   int64
	WinnerName string
}

// activeTrivias tracks in-flight trivia questions keyed by message_id.
// Same shape and concurrency contract as quiz.ActiveQuizzes; kept local
// to the bot package so the quiz domain package is not modified.
type activeTrivias struct {
	mu    sync.Mutex
	byMsg map[int64]*activeTrivia
}

func newActiveTrivias() *activeTrivias {
	return &activeTrivias{byMsg: make(map[int64]*activeTrivia)}
}

func (a *activeTrivias) register(q *activeTrivia) {
	if q == nil || q.MessageID == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.byMsg[q.MessageID] = q
}

func (a *activeTrivias) get(messageID int64) *activeTrivia {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.byMsg[messageID]
}

// markSolved atomically records the first solver. Returns true only for
// the first caller.
func (a *activeTrivias) markSolved(messageID, userID int64, name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	q, ok := a.byMsg[messageID]
	if !ok || q.WinnerID != 0 {
		return false
	}
	q.WinnerID = userID
	q.WinnerName = name
	return true
}

func (a *activeTrivias) forget(messageID int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.byMsg, messageID)
}

func (a *activeTrivias) active() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.byMsg)
}

// TriviaHandler implements "/trivia" and "/trivia top". Question state
// during play lives in *activeTrivias; the leaderboard reuses quiz.Store
// (the same per-chat "quiz mastery" board as the code quiz) so wins from
// both games accrue together and no new bbolt bucket is required.
type TriviaHandler struct {
	active *activeTrivias
	repo   quiz.Store
	bot    triviaSender
	log    *slog.Logger

	// rand lets tests inject a deterministic source; nil falls back to a
	// fresh time-seeded *rand.Rand per dispatch.
	rand *rand.Rand
}

func NewTriviaHandler(repo quiz.Store, bot triviaSender, log *slog.Logger) *TriviaHandler {
	if log == nil {
		log = slog.Default()
	}
	return &TriviaHandler{
		active: newActiveTrivias(),
		repo:   repo,
		bot:    bot,
		log:    log,
	}
}

// HandleTrivia routes "/trivia" and "/trivia top".
func (h *TriviaHandler) HandleTrivia(_ *th.Context, msg telego.Message) error {
	if msg.From == nil || msg.From.IsBot {
		return nil
	}
	parts := strings.Fields(msg.Text)
	if len(parts) >= 2 && strings.EqualFold(parts[1], "top") {
		return h.handleTop(msg)
	}
	return h.handleStart(msg)
}

func (h *TriviaHandler) handleStart(msg telego.Message) error {
	r := h.rand
	if r == nil {
		r = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	idx := quiz.PickRandomTrivia(r)
	labels, correctIdx, err := quiz.BuildTriviaOptions(idx, r)
	if err != nil {
		h.log.Warn("trivia BuildTriviaOptions failed", "error", err, "trivia_idx", idx)
		return h.replyText(msg.Chat.ID, msg.MessageID, publicPureFailure())
	}
	tr, _ := quiz.GetTrivia(idx)

	body := renderTriviaBody(tr.Question)
	kb := renderTriviaKeyboard(idx, labels)

	sent, err := h.bot.SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID:      telego.ChatID{ID: msg.Chat.ID},
		Text:        body,
		ParseMode:   telego.ModeHTML,
		ReplyMarkup: kb,
	})
	if err != nil {
		h.log.Warn("trivia send failed", "error", err, "chat_id", msg.Chat.ID)
		_ = h.replyText(msg.Chat.ID, msg.MessageID, publicPureFailure())
		return nil
	}

	h.active.register(&activeTrivia{
		MessageID:  int64(sent.MessageID),
		AbsChatID:  storage.AbsChatID(msg.Chat.ID),
		TriviaIdx:  idx,
		CorrectIdx: correctIdx,
		Options:    labels,
		StartedAt:  time.Now().UTC(),
	})
	return nil
}

func (h *TriviaHandler) handleTop(msg telego.Message) error {
	absChatID := storage.AbsChatID(msg.Chat.ID)
	entries, err := h.repo.TopByChat(context.Background(), absChatID, 5)
	if err != nil {
		h.log.Warn("trivia TopByChat failed", "error", err, "chat_id", absChatID)
		return h.replyText(msg.Chat.ID, msg.MessageID, "Не удалось получить топ. Попробуйте позже.")
	}
	_, err = h.bot.SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: msg.Chat.ID},
		Text:      renderTriviaTop(entries),
		ParseMode: telego.ModeHTML,
		ReplyParameters: &telego.ReplyParameters{
			MessageID: msg.MessageID,
		},
	})
	return err
}

// TriviaCallbackPredicate matches only callback_data under the trivia
// verb ("g1:t:"). Register this handler BEFORE the quiz callback so
// telego's first-match-wins routing keeps the two games separate even
// though both share the "g1:" namespace.
func TriviaCallbackPredicate() th.Predicate {
	return func(_ context.Context, update telego.Update) bool {
		cb := update.CallbackQuery
		if cb == nil {
			return false
		}
		return strings.HasPrefix(cb.Data, GamesCallbackPrefix+triviaCallbackVerb+":")
	}
}

// HandleCallback is the th.CallbackQueryHandler for trivia buttons.
// Mirrors QuizHandler.HandleCallback: validate, first-correct-tap wins,
// bump the leaderboard, edit the message to show the outcome.
func (h *TriviaHandler) HandleCallback(ctx *th.Context, query telego.CallbackQuery) error {
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
		h.log.Warn("trivia EditMessageText failed", "error", err)
	}
	return nil
}

type triviaCallbackResponse struct {
	Toast      string
	Alert      bool
	EditedText string
}

func (h *TriviaHandler) dispatchCallback(ctx context.Context, query telego.CallbackQuery) triviaCallbackResponse {
	triviaIdx, chosenIdx, ok := parseTriviaCallback(query.Data)
	if !ok {
		return triviaCallbackResponse{Toast: "Эта кнопка устарела."}
	}
	if query.Message == nil {
		return triviaCallbackResponse{Toast: "Сообщение недоступно."}
	}
	msgID := int64(query.Message.GetMessageID())

	q := h.active.get(msgID)
	if q == nil {
		return triviaCallbackResponse{Toast: "Викторина больше не активна."}
	}
	if q.TriviaIdx != triviaIdx {
		return triviaCallbackResponse{Toast: "Кнопка не от этого вопроса."}
	}
	if q.WinnerID != 0 {
		return triviaCallbackResponse{Toast: "Уже отвечено - " + q.WinnerName + "."}
	}
	if chosenIdx < 0 || chosenIdx >= len(q.Options) {
		return triviaCallbackResponse{Toast: "Неверная кнопка."}
	}

	user := query.From
	display := shared.UserDisplay(user.Username, user.FirstName)
	if display == "" {
		display = fmt.Sprintf("user %d", user.ID)
	}

	if chosenIdx != q.CorrectIdx {
		return triviaCallbackResponse{Toast: "Не верно. Попробуйте ещё раз."}
	}

	if !h.active.markSolved(msgID, user.ID, display) {
		return triviaCallbackResponse{Toast: "Уже ответили."}
	}

	tr, err := quiz.GetTrivia(q.TriviaIdx)
	if err != nil {
		h.log.Warn("trivia callback: question missing on resolve", "trivia_idx", q.TriviaIdx)
		return triviaCallbackResponse{Toast: "Вопрос пропал."}
	}

	if err := h.repo.IncrementCorrect(ctx, quiz.Entry{
		AbsChatID:    q.AbsChatID,
		UserID:       user.ID,
		Username:     user.Username,
		FirstName:    user.FirstName,
		LastPlayedAt: time.Now().UTC(),
	}); err != nil {
		h.log.Warn("trivia IncrementCorrect failed", "error", err, "user_id", user.ID, "chat_id", q.AbsChatID)
		// Still announce the win even if the leaderboard write failed.
	}

	h.active.forget(msgID)

	correctAnswer := q.Options[q.CorrectIdx]
	body := fmt.Sprintf(
		"%s\n\n✅ Первым ответил %s - правильно: %s.",
		renderTriviaBody(tr.Question),
		display,
		html.EscapeString(correctAnswer),
	)
	return triviaCallbackResponse{Toast: "Верно!", EditedText: body}
}

func renderTriviaBody(question string) string {
	return "❓ <b>IT-викторина</b>\n\n" + html.EscapeString(question)
}

// renderTriviaKeyboard builds the four-button keyboard. callback_data:
// "g1:t:<triviaIdx>:<chosenIdx>" - triviaIdx <= 3 digits, chosenIdx 1
// digit, prefix 5 chars: well under Telegram's 64-byte limit.
func renderTriviaKeyboard(triviaIdx int, labels []string) *telego.InlineKeyboardMarkup {
	row := make([]telego.InlineKeyboardButton, 0, 4)
	for i, l := range labels {
		row = append(row, telego.InlineKeyboardButton{
			Text:         l,
			CallbackData: fmt.Sprintf("%s%s:%d:%d", GamesCallbackPrefix, triviaCallbackVerb, triviaIdx, i),
		})
	}
	return &telego.InlineKeyboardMarkup{
		InlineKeyboard: [][]telego.InlineKeyboardButton{row[:2], row[2:]},
	}
}

func renderTriviaTop(entries []quiz.Entry) string {
	if len(entries) == 0 {
		return "❓ <b>Топ викторины</b>\n\nПока никто не ответил правильно."
	}
	var b strings.Builder
	b.WriteString("❓ <b>Топ викторины</b>\n\n")
	for i, e := range entries {
		display := shared.UserDisplay(e.Username, e.FirstName)
		if display == "" {
			display = fmt.Sprintf("user %d", e.UserID)
		}
		fmt.Fprintf(&b, "%d. %s - %d\n", i+1, display, e.CorrectCount)
	}
	return b.String()
}

// parseTriviaCallback parses "g1:t:<triviaIdx>:<chosenIdx>". Mirrors
// parseQuizCallback's shape exactly (just a different verb).
func parseTriviaCallback(data string) (triviaIdx, chosenIdx int, ok bool) {
	prefix := GamesCallbackPrefix + triviaCallbackVerb + ":"
	if !strings.HasPrefix(data, prefix) {
		return 0, 0, false
	}
	rest := data[len(prefix):]
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

// makeTriviaCallback mirrors makeQuizCallback for in-package tests.
func makeTriviaCallback(triviaIdx, chosenIdx int) string {
	return fmt.Sprintf("%s%s:%d:%d", GamesCallbackPrefix, triviaCallbackVerb, triviaIdx, chosenIdx)
}

func (h *TriviaHandler) replyText(chatID int64, replyTo int, body string) error {
	_, err := h.bot.SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: chatID},
		Text:   body,
		ReplyParameters: &telego.ReplyParameters{
			MessageID: replyTo,
		},
	})
	return err
}
