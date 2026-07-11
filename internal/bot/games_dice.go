package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/games/dice"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
)

// diceSender is the narrow telego surface the dice handler needs.
// Declared here so tests can substitute a recording stub for the real
// *telego.Bot without dragging in the full API surface.
type diceSender interface {
	SendDice(ctx context.Context, params *telego.SendDiceParams) (*telego.Message, error)
	SendMessage(ctx context.Context, params *telego.SendMessageParams) (*telego.Message, error)
}

// DiceHandler wires the /dice slash command to the dice domain. It
// performs the SendDice call (Telegram returns the rolled value
// immediately even though the animation lasts ~3-5s), records the
// result in the leaderboard, and posts an announcement when a new
// chat record is set.
type DiceHandler struct {
	svc *dice.Service
	bot diceSender
	log *slog.Logger
}

func NewDiceHandler(svc *dice.Service, bot diceSender, log *slog.Logger) *DiceHandler {
	if log == nil {
		log = slog.Default()
	}
	return &DiceHandler{svc: svc, bot: bot, log: log}
}

// HandleDice handles "/dice" and "/dice <emoji>". Validation rules:
//   - emoji argument, if present, must be one of the six allowed dice emojis;
//     anything else gets a friendly hint and the request is dropped.
//   - empty argument defaults to 🎲.
//
// The handler does not depend on admin status - dice is for everyone.
func (h *DiceHandler) HandleDice(_ *th.Context, msg telego.Message) error {
	if msg.From == nil {
		// Anonymous admins, channel forwards, etc. Just ignore - dice
		// without a user has nowhere to attribute the score.
		return nil
	}

	emoji := dice.DefaultEmoji
	parts := strings.Fields(msg.Text)
	if len(parts) >= 2 {
		emoji = strings.TrimSpace(parts[1])
		if !dice.IsAllowedEmoji(emoji) {
			return h.replyText(msg, diceHintMessage())
		}
	}

	bgCtx := context.Background()
	rolled, err := h.bot.SendDice(bgCtx, &telego.SendDiceParams{
		ChatID: telego.ChatID{ID: msg.Chat.ID},
		Emoji:  emoji,
	})
	if err != nil {
		h.log.Warn("sendDice failed", "error", err, "chat_id", msg.Chat.ID)
		return h.replyText(msg, publicPureFailure())
	}
	if rolled == nil || rolled.Dice == nil {
		h.log.Warn("sendDice returned no dice", "chat_id", msg.Chat.ID)
		return nil
	}

	value := rolled.Dice.Value
	absChatID := storage.AbsChatID(msg.Chat.ID)
	ts := time.Unix(int64(rolled.Date), 0).UTC()
	if ts.IsZero() {
		ts = time.Now().UTC()
	}

	outcome, err := h.svc.SubmitRoll(bgCtx, absChatID, emoji, value, msg.From.ID, msg.From.Username, msg.From.FirstName, ts)
	if err != nil {
		// Validation/storage error: dice was already rolled and shown
		// in the chat, so we just log and skip the announcement.
		h.log.Warn("dice SubmitRoll failed", "error", err, "chat_id", absChatID, "user_id", msg.From.ID, "value", value)
		return nil
	}

	announcement := buildDiceAnnouncement(emoji, value, msg.From, outcome)
	if announcement == "" {
		return nil
	}
	return h.replyToMessage(msg.Chat.ID, rolled.MessageID, announcement)
}

// buildDiceAnnouncement returns the text the bot posts after the dice
// animation. Returns "" when nothing notable happened (a roll lower
// than the chat record without any record being set).
func buildDiceAnnouncement(emoji string, value int, from *telego.User, outcome *dice.RollOutcome) string {
	if outcome == nil {
		return ""
	}
	display := shared.UserDisplay(from.Username, from.FirstName)
	maxV := dice.MaxValue[emoji]
	switch {
	case outcome.NewRecord && outcome.Previous == nil:
		return fmt.Sprintf("%s Первый рекорд %s в этом чате - %d/%d у %s.",
			emoji, emoji, value, maxV, display)
	case outcome.NewRecord:
		return fmt.Sprintf("%s Новый рекорд %s - %d/%d у %s. Прежний: %d, держал %s.",
			emoji, emoji, value, maxV, display, outcome.Previous.Value, displayFromRecord(outcome.Previous))
	case outcome.Tied:
		return fmt.Sprintf("%s Повтор рекорда %d/%d у %s. Текущий держатель: %s.",
			emoji, value, maxV, display, displayFromRecord(&outcome.Recorded))
	default:
		// Below the record: stay quiet. The dice itself was rendered;
		// no need to spam another message in a 200-member chat.
		return ""
	}
}

// displayFromRecord renders the holder of a leaderboard record. Falls
// back to "user N" when neither username nor first name is known.
func displayFromRecord(r *dice.Record) string {
	if r == nil {
		return ""
	}
	d := shared.UserDisplay(r.Username, r.FirstName)
	if d == "" {
		return fmt.Sprintf("user %d", r.UserID)
	}
	return d
}

// diceHintMessage explains the supported emoji set when the user types
// an unsupported value.
func diceHintMessage() string {
	emojis := strings.Join(dice.AllowedEmojis, " ")
	return "Неверный смайл. Поддерживаются: " + emojis + ". Без аргумента - обычный кубик 🎲."
}

func (h *DiceHandler) replyText(msg telego.Message, body string) error {
	_, err := h.bot.SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: msg.Chat.ID},
		Text:   body,
		ReplyParameters: &telego.ReplyParameters{
			MessageID: msg.MessageID,
		},
	})
	return err
}

func (h *DiceHandler) replyToMessage(chatID int64, replyTo int, body string) error {
	_, err := h.bot.SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: chatID},
		Text:      body,
		ParseMode: telego.ModeHTML,
		ReplyParameters: &telego.ReplyParameters{
			MessageID: replyTo,
		},
	})
	return err
}
