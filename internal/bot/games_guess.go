package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/games/guess"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
)

// guessSender is the narrow telego surface the guess handler needs.
// Declared here so tests can substitute a recording stub.
type guessSender interface {
	SendMessage(ctx context.Context, params *telego.SendMessageParams) (*telego.Message, error)
}

// GuessHandler wires "/guess" and "/guess N" to the guess domain.
//
//   - "/guess"            starts a round, or shows status if one is active.
//   - "/guess top"        shows the per-chat win leaderboard.
//   - "/guess <number>"   submits a guess against the active round.
//
// The game is open to everyone (no admin gate); the per-user cooldown
// configured in registerGameRoutes is the only flood protection.
type GuessHandler struct {
	svc *guess.Service
	bot guessSender
	log *slog.Logger
}

func NewGuessHandler(svc *guess.Service, bot guessSender, log *slog.Logger) *GuessHandler {
	if log == nil {
		log = slog.Default()
	}
	return &GuessHandler{svc: svc, bot: bot, log: log}
}

// HandleGuess routes the /guess command variants.
func (h *GuessHandler) HandleGuess(_ *th.Context, msg telego.Message) error {
	if msg.From == nil || msg.From.IsBot {
		// No user to attribute a guess/win to.
		return nil
	}

	parts := strings.Fields(msg.Text)
	bgCtx := context.Background()
	absChatID := storage.AbsChatID(msg.Chat.ID)

	// "/guess" with no argument: status or start.
	if len(parts) < 2 {
		return h.startOrStatus(bgCtx, msg, absChatID)
	}

	arg := strings.TrimSpace(parts[1])
	if strings.EqualFold(arg, "top") {
		return h.handleTop(bgCtx, msg, absChatID)
	}

	// Numeric guess.
	n, convErr := strconv.Atoi(arg)
	if convErr != nil {
		return h.reply(msg, fmt.Sprintf(
			"Это не число. Угадайте число от %d до %d: /guess 42", guess.Min, guess.Max))
	}
	if n < guess.Min || n > guess.Max {
		return h.reply(msg, fmt.Sprintf(
			"Число вне диапазона. Загадано от %d до %d.", guess.Min, guess.Max))
	}

	out, err := h.svc.Guess(bgCtx, absChatID, n, msg.From.ID, msg.From.Username, msg.From.FirstName, time.Unix(int64(msg.Date), 0).UTC())
	if err == guess.ErrNotFound {
		return h.reply(msg, "Сейчас нет активного раунда. Запустите: /guess")
	}
	if err != nil {
		h.log.Warn("guess Guess failed", "error", err, "chat_id", absChatID, "value", n)
		return h.reply(msg, "Не удалось обработать догадку. Попробуйте ещё раз.")
	}

	display := shared.UserDisplay(msg.From.Username, msg.From.FirstName)
	switch {
	case out.Correct:
		return h.reply(msg, fmt.Sprintf(
			"🎉 %s угадал! Это было <b>%d</b>. Попыток в раунде: %d. Новый раунд: /guess",
			display, out.Secret, out.Attempts))
	case out.TooLow:
		return h.reply(msg, fmt.Sprintf("📈 %d - мало. Загаданное больше.", n))
	default: // TooHigh
		return h.reply(msg, fmt.Sprintf("📉 %d - много. Загаданное меньше.", n))
	}
}

// startOrStatus starts a new round or, if one is active, reports its
// status without leaking the secret.
func (h *GuessHandler) startOrStatus(ctx context.Context, msg telego.Message, absChatID int64) error {
	now := time.Unix(int64(msg.Date), 0).UTC()
	out, err := h.svc.Start(ctx, absChatID, now)
	if err != nil {
		h.log.Warn("guess Start failed", "error", err, "chat_id", absChatID)
		return h.reply(msg, "Не удалось запустить игру. Попробуйте позже.")
	}
	if !out.Started {
		// Round already running: show status, keep the secret.
		attempts := 0
		if out.Existing != nil {
			attempts = out.Existing.Attempts
		}
		return h.reply(msg, fmt.Sprintf(
			"Раунд уже идёт. Загадано число от %d до %d. Попыток: %d. Угадывайте: /guess 50",
			guess.Min, guess.Max, attempts))
	}
	prefix := ""
	if out.Recycled {
		prefix = "Прошлый раунд завис и был сброшен. "
	}
	return h.reply(msg, fmt.Sprintf(
		"%s🎯 Загадал число от %d до %d. Угадывайте командой: /guess 50",
		prefix, guess.Min, guess.Max))
}

func (h *GuessHandler) handleTop(ctx context.Context, msg telego.Message, absChatID int64) error {
	entries, err := h.svc.Top(ctx, absChatID, 5)
	if err != nil {
		h.log.Warn("guess Top failed", "error", err, "chat_id", absChatID)
		return h.reply(msg, "Не удалось получить топ. Попробуйте позже.")
	}
	return h.reply(msg, renderGuessTop(entries))
}

func renderGuessTop(entries []guess.WinEntry) string {
	if len(entries) == 0 {
		return "🎯 <b>Топ угадайки</b>\n\nПока никто не угадал ни одного числа."
	}
	var b strings.Builder
	b.WriteString("🎯 <b>Топ угадайки</b>\n\n")
	for i, e := range entries {
		display := shared.UserDisplay(e.Username, e.FirstName)
		if display == "" {
			display = fmt.Sprintf("user %d", e.UserID)
		}
		fmt.Fprintf(&b, "%d. %s - %d\n", i+1, display, e.Wins)
	}
	return b.String()
}

func (h *GuessHandler) reply(msg telego.Message, body string) error {
	_, err := h.bot.SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: msg.Chat.ID},
		Text:      body,
		ParseMode: telego.ModeHTML,
		ReplyParameters: &telego.ReplyParameters{
			MessageID: msg.MessageID,
		},
	})
	return err
}
