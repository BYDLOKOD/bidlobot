package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/games/hangman"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
)

// hangmanSender is the narrow telego surface the hangman handler needs.
type hangmanSender interface {
	SendMessage(ctx context.Context, params *telego.SendMessageParams) (*telego.Message, error)
}

// HangmanHandler wires "/hangman" and "/hangman X" to the hangman
// domain.
//
//   - "/hangman"      starts a round, or shows the board if one is active.
//   - "/hangman <c>"  guesses a single letter (Latin or Cyrillic).
//
// One round per chat; open to everyone (cooldown is the only throttle).
type HangmanHandler struct {
	svc *hangman.Service
	bot hangmanSender
	log *slog.Logger
}

func NewHangmanHandler(svc *hangman.Service, bot hangmanSender, log *slog.Logger) *HangmanHandler {
	if log == nil {
		log = slog.Default()
	}
	return &HangmanHandler{svc: svc, bot: bot, log: log}
}

// HandleHangman routes the /hangman command variants.
func (h *HangmanHandler) HandleHangman(_ *th.Context, msg telego.Message) error {
	if msg.From == nil || msg.From.IsBot {
		return nil
	}

	parts := strings.Fields(msg.Text)
	bgCtx := context.Background()
	absChatID := storage.AbsChatID(msg.Chat.ID)

	if len(parts) < 2 {
		return h.startOrStatus(bgCtx, msg, absChatID)
	}

	letter := strings.TrimSpace(parts[1])
	out, err := h.svc.Guess(bgCtx, absChatID, letter)
	switch err {
	case nil:
		// fall through to rendering
	case hangman.ErrNotFound:
		return h.reply(msg, "Нет активной игры. Запустите: /hangman")
	case hangman.ErrBadLetter:
		return h.reply(msg, "Нужна ровно одна буква (русская или латинская). Пример: /hangman а")
	case hangman.ErrAlreadyUsed:
		return h.reply(msg, fmt.Sprintf("Букву «%s» уже называли. Попробуйте другую.", strings.ToUpper(letter)))
	default:
		h.log.Warn("hangman Guess failed", "error", err, "chat_id", absChatID)
		return h.reply(msg, "Не удалось обработать букву. Попробуйте ещё раз.")
	}

	display := shared.UserDisplay(msg.From.Username, msg.From.FirstName)
	switch out.Result {
	case hangman.GuessWon:
		return h.reply(msg, fmt.Sprintf(
			"🎉 %s отгадал слово: <b>%s</b>!\nНовая игра: /hangman",
			display, shared.EscapeHTML(out.Word)))
	case hangman.GuessLost:
		return h.reply(msg, fmt.Sprintf(
			"💀 Виселица. Слово было: <b>%s</b>.\nНовая игра: /hangman",
			shared.EscapeHTML(out.Word)))
	case hangman.GuessHit:
		return h.reply(msg, renderHangmanBoard("✅ Есть буква!", out.Masked, out.UsedLetters, out.WrongLeft))
	default: // GuessMiss
		return h.reply(msg, renderHangmanBoard("❌ Мимо.", out.Masked, out.UsedLetters, out.WrongLeft))
	}
}

func (h *HangmanHandler) startOrStatus(ctx context.Context, msg telego.Message, absChatID int64) error {
	now := time.Unix(int64(msg.Date), 0).UTC()
	out, err := h.svc.Start(ctx, absChatID, now)
	if err != nil {
		h.log.Warn("hangman Start failed", "error", err, "chat_id", absChatID)
		return h.reply(msg, "Не удалось запустить игру. Попробуйте позже.")
	}
	if !out.Started {
		// Round already running: show the current board.
		r := out.Existing
		return h.reply(msg, renderHangmanBoard(
			"Игра уже идёт. Называйте буквы: /hangman а",
			hangman.MaskFor(r),
			hangman.SortedUsed(r),
			hangman.MaxWrong-r.WrongCount,
		))
	}
	// Fresh round: show the fully masked board. Re-fetch state via
	// Status so the mask reflects the freshly stored word.
	st, err := h.svc.Status(ctx, absChatID)
	if err != nil || st == nil {
		// Defensive: the round was just created; if Status hiccups we
		// still tell the user the game started.
		prefix := ""
		if out.Recycled {
			prefix = "Прошлая игра зависла и была сброшена. "
		}
		return h.reply(msg, prefix+"🪢 Виселица началась! Называйте буквы: /hangman а")
	}
	prefix := "🪢 Новая виселица!"
	if out.Recycled {
		prefix = "Прошлая игра зависла и была сброшена. 🪢 Новая виселица!"
	}
	return h.reply(msg, renderHangmanBoard(
		prefix+" Называйте буквы: /hangman а",
		hangman.MaskFor(st),
		hangman.SortedUsed(st),
		hangman.MaxWrong-st.WrongCount,
	))
}

// renderHangmanBoard formats the masked word, the used letters and the
// remaining-tries gauge inside an HTML <code> block for monospace
// alignment.
func renderHangmanBoard(headline, masked string, used []string, wrongLeft int) string {
	spaced := strings.Join(strings.Split(masked, ""), " ")
	usedLine := "-"
	if len(used) > 0 {
		usedLine = strings.Join(used, " ")
	}
	return fmt.Sprintf(
		"🪢 <b>Виселица</b>\n%s\n\n<code>%s</code>\n\nБуквы: %s\nОшибок осталось: %d/%d",
		headline,
		shared.EscapeHTML(spaced),
		shared.EscapeHTML(usedLine),
		wrongLeft, hangman.MaxWrong,
	)
}

func (h *HangmanHandler) reply(msg telego.Message, body string) error {
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
