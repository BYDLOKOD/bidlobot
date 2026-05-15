package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/games/battle"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
)

// battleSender is the narrow telego surface BattleHandler needs. Tests
// supply a recording stub so the goroutine flow is deterministic
// without a live bot.
type battleSender interface {
	SendMessage(ctx context.Context, params *telego.SendMessageParams) (*telego.Message, error)
}

// battleClock abstracts time so tests can advance the 60-second window
// without sleeping. Production wiring uses realClock.
type battleClock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now().UTC() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// BattleHandler implements /battle X Y. It posts two side messages,
// schedules the timer goroutine, and exposes ObserveReaction for the
// reaction observer to call on every message_reaction update.
type BattleHandler struct {
	registry *battle.Registry
	bot      battleSender
	clock    battleClock
	log      *slog.Logger

	// duration overrides battle.DefaultDuration. Tests set this to
	// something tiny.
	duration time.Duration

	// nextID is the function used to generate per-battle IDs; pluggable
	// so tests can produce deterministic IDs without crypto/rand.
	nextID func() (string, error)
}

// NewBattleHandler wires the production handler. duration<=0 falls back
// to battle.DefaultDuration.
func NewBattleHandler(registry *battle.Registry, bot battleSender, log *slog.Logger) *BattleHandler {
	if log == nil {
		log = slog.Default()
	}
	return &BattleHandler{
		registry: registry,
		bot:      bot,
		clock:    realClock{},
		log:      log,
		duration: battle.DefaultDuration,
		nextID:   storage.NewID,
	}
}

// HandleBattle is the th.MessageHandler for "/battle". Validates labels,
// posts the two side messages, schedules the close goroutine. Errors
// during posting fall through to a friendly reply; the partial battle
// is rolled back so the registry has no half-state.
func (h *BattleHandler) HandleBattle(_ *th.Context, msg telego.Message) error {
	if msg.From == nil || msg.From.IsBot {
		return nil
	}

	left, right, ok := parseBattleArgs(msg.Text)
	if !ok {
		return h.replyText(msg.Chat.ID, msg.MessageID, battleHintMessage())
	}
	if len(left) > battle.MaxLabelLen || len(right) > battle.MaxLabelLen {
		return h.replyText(msg.Chat.ID, msg.MessageID,
			fmt.Sprintf("Стороны слишком длинные (максимум %d символов на каждую).", battle.MaxLabelLen))
	}

	bgCtx := context.Background()
	id, err := h.nextID()
	if err != nil {
		h.log.Warn("battle id generation failed", "error", err)
		return h.replyText(msg.Chat.ID, msg.MessageID, "Не удалось запустить баттл. Попробуйте ещё раз.")
	}

	absChatID := storage.AbsChatID(msg.Chat.ID)
	now := h.clock.Now()
	b, err := battle.NewBattle(id, absChatID, left, right, now, h.duration)
	if err != nil {
		return h.replyText(msg.Chat.ID, msg.MessageID, "Стороны не должны быть пустыми.")
	}
	h.registry.Add(b)

	header := fmt.Sprintf("\U0001F94A <b>%s vs %s</b>\nГолосуйте реакциями за %s.\nКаждый участник учитывается один раз на сторону.",
		shared.EscapeHTML(left), shared.EscapeHTML(right), formatBattleWindow(h.duration))
	if _, sendErr := h.bot.SendMessage(bgCtx, &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: msg.Chat.ID},
		Text:      header,
		ParseMode: telego.ModeHTML,
		ReplyParameters: &telego.ReplyParameters{
			MessageID: msg.MessageID,
		},
	}); sendErr != nil {
		h.registry.Remove(id)
		h.log.Warn("battle header send failed", "error", sendErr, "chat_id", msg.Chat.ID)
		_ = h.replyText(msg.Chat.ID, msg.MessageID, "Не удалось запустить баттл. Попробуйте ещё раз.")
		return nil
	}

	leftMsg, err := h.bot.SendMessage(bgCtx, &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: msg.Chat.ID},
		Text:      fmt.Sprintf("За <b>%s</b> - реакция на это сообщение.", shared.EscapeHTML(left)),
		ParseMode: telego.ModeHTML,
	})
	if err != nil {
		h.registry.Remove(id)
		h.log.Warn("battle left side send failed", "error", err, "chat_id", msg.Chat.ID)
		_ = h.replyText(msg.Chat.ID, msg.MessageID, "Не удалось запустить баттл. Попробуйте ещё раз.")
		return nil
	}

	rightMsg, err := h.bot.SendMessage(bgCtx, &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: msg.Chat.ID},
		Text:      fmt.Sprintf("За <b>%s</b> - реакция на это сообщение.", shared.EscapeHTML(right)),
		ParseMode: telego.ModeHTML,
	})
	if err != nil {
		h.registry.Remove(id)
		h.log.Warn("battle right side send failed", "error", err, "chat_id", msg.Chat.ID)
		_ = h.replyText(msg.Chat.ID, msg.MessageID, "Не удалось запустить баттл. Попробуйте ещё раз.")
		return nil
	}

	h.registry.SetMessageIDs(id, leftMsg.MessageID, rightMsg.MessageID)

	// Subscribe to the timer BEFORE spawning the goroutine so tests
	// (and any other ordering-sensitive caller) see the After()
	// invocation happen synchronously inside HandleBattle.
	timer := h.clock.After(h.duration)
	go h.runCloseTimer(id, msg.Chat.ID, left, right, timer)
	return nil
}

// runCloseTimer waits the configured duration and announces the result
// in the same chat. Any storage of running state is in-memory only;
// crashing while a battle is in flight loses it (acceptable for a
// 60-second window with no real consequences).
func (h *BattleHandler) runCloseTimer(id string, chatID int64, left, right string, timer <-chan time.Time) {
	<-timer
	b := h.registry.Get(id)
	if b == nil {
		// Removed externally (e.g. by tests). Nothing to announce.
		return
	}
	res := b.Tally(h.clock.Now())
	body := renderBattleResult(res, left, right)
	if _, err := h.bot.SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: chatID},
		Text:      body,
		ParseMode: telego.ModeHTML,
	}); err != nil {
		h.log.Warn("battle close announcement failed", "error", err, "chat_id", chatID, "battle_id", id)
	}
	h.registry.Remove(id)
}

// ObserveReaction is invoked from the message_reaction handler chain on
// every reaction. It looks up the active battle by message_id and, if
// the user added a reaction (NewReaction non-empty after the change),
// records a vote. Removing a reaction does NOT decrement - the rule is
// "ever reacted during the window".
func (h *BattleHandler) ObserveReaction(_ *th.Context, reaction telego.MessageReactionUpdated) error {
	if reaction.User == nil || reaction.User.IsBot {
		return nil
	}
	if len(reaction.NewReaction) == 0 {
		// Reaction removed - no decrement, no other side effect.
		return nil
	}
	b, side, ok := h.registry.LookupByMessageID(reaction.MessageID)
	if !ok {
		return nil
	}
	if storage.AbsChatID(reaction.Chat.ID) != b.AbsChatID {
		return nil
	}
	now := h.clock.Now()
	if now.After(b.EndsAt) {
		// Late reaction (e.g. delivered after the timer fired). Ignore.
		return nil
	}
	b.RecordVote(reaction.User.ID, side)
	return nil
}

// renderBattleResult formats the closing announcement. left/right are
// passed in addition to the result's labels so we can show a final
// recap even when the labels in the Result struct were trimmed.
func renderBattleResult(r *battle.Result, leftRaw, rightRaw string) string {
	left := r.LeftLabel
	if left == "" {
		left = leftRaw
	}
	right := r.RightLabel
	if right == "" {
		right = rightRaw
	}
	left = shared.EscapeHTML(left)
	right = shared.EscapeHTML(right)

	switch {
	case r.NoVotes:
		return fmt.Sprintf("\U0001F94A <b>%s vs %s</b>\nНикто не проголосовал. Ничья по умолчанию.", left, right)
	case r.Tied:
		return fmt.Sprintf("\U0001F94A <b>%s vs %s</b>\nНичья - %d:%d.", left, right, r.LeftVotes, r.RightVotes)
	case r.WinnerSide == battle.SideLeft:
		return fmt.Sprintf("\U0001F94A <b>%s vs %s</b>\nПобеждает <b>%s</b> - %d:%d.", left, right, left, r.LeftVotes, r.RightVotes)
	default:
		return fmt.Sprintf("\U0001F94A <b>%s vs %s</b>\nПобеждает <b>%s</b> - %d:%d.", left, right, right, r.LeftVotes, r.RightVotes)
	}
}

// parseBattleArgs splits "/battle X Y" into left and right. Each side
// may be a single token (for the typical "go vs rust" / "👍 vs 👎" case);
// extra tokens after the second are joined as part of the second side.
// This lets users write "/battle Go Rust" or "/battle Go Rust language"
// and the second side absorbs the trailing word.
func parseBattleArgs(text string) (left, right string, ok bool) {
	parts := strings.Fields(text)
	if len(parts) < 3 {
		return "", "", false
	}
	left = parts[1]
	right = strings.Join(parts[2:], " ")
	if left == "" || right == "" {
		return "", "", false
	}
	return left, right, true
}

// formatBattleWindow renders a duration as a Russian short label. We
// keep it custom instead of reusing inline.go's formatDuration because
// "60s" reads better as "60с" than "1м" in this context.
func formatBattleWindow(d time.Duration) string {
	if d == battle.DefaultDuration {
		return "60с"
	}
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%dм", int(d/time.Minute))
	}
	if d%time.Second == 0 {
		return fmt.Sprintf("%dс", int(d/time.Second))
	}
	return d.String()
}

// battleHintMessage explains the expected /battle syntax.
func battleHintMessage() string {
	return "Использование: /battle X Y - две стороны (одно слово или эмодзи каждая). Пример: /battle Go Rust."
}

func (h *BattleHandler) replyText(chatID int64, replyTo int, body string) error {
	_, err := h.bot.SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: chatID},
		Text:   body,
		ReplyParameters: &telego.ReplyParameters{
			MessageID: replyTo,
		},
	})
	return err
}
