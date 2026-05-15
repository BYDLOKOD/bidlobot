package bot

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/games/dice"
	"github.com/veschin/bidlobot/internal/games/duel"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
)

// duelSender is the narrow telego surface the duel handler needs. It
// mirrors how DiceHandler uses SendDice: Telegram returns the rolled
// value synchronously in the Message.Dice even though the animation
// lasts a few seconds, so two sequential SendDice calls give us both
// duel rolls without any state or callback.
type duelSender interface {
	SendDice(ctx context.Context, params *telego.SendDiceParams) (*telego.Message, error)
	SendMessage(ctx context.Context, params *telego.SendMessageParams) (*telego.Message, error)
}

// duelMembers is the narrow membership surface the duel handler needs
// to verify the opponent is a real member of THIS chat. Mirrors the
// moderation executor's resolveTarget pattern (the bot only knows users
// it observed at least once). storage.MembershipRepo satisfies it.
type duelMembers interface {
	GetMemberByUsername(ctx context.Context, absChatID int64, username string) (*membership.Member, error)
}

// DuelHandler wires "/duel @user" to an immediate two-dice resolution.
// There is no accept step (which would require persisted state and a
// callback we cannot register from here): the caller invokes, the bot
// rolls one die for the challenger and one for the opponent, and the
// higher roll wins. Open to everyone (cooldown is the throttle).
type DuelHandler struct {
	bot     duelSender
	members duelMembers
	log     *slog.Logger

	// botUsername is used to reject "/duel @thebot". Empty disables the
	// check (bot identity unknown at construction).
	botUsername string
}

// NewDuelHandler wires the handler. members validates that the opponent
// is a real member of the chat (the bot can only duel users it has
// observed); it must not be nil in production. botUsername may be empty;
// pass the value from GetMe at startup so members cannot duel the bot.
func NewDuelHandler(bot duelSender, members duelMembers, botUsername string, log *slog.Logger) *DuelHandler {
	if log == nil {
		log = slog.Default()
	}
	return &DuelHandler{bot: bot, members: members, log: log, botUsername: botUsername}
}

// HandleDuel handles "/duel @user". Validation:
//   - an @username (or bare handle) must be present  -> ErrNoTarget
//   - it must not be the caller                       -> ErrSelfTarget
//   - it must not be the bot                          -> ErrBotTarget
func (h *DuelHandler) HandleDuel(_ *th.Context, msg telego.Message) error {
	if msg.From == nil || msg.From.IsBot {
		return nil
	}

	op, err := duel.ParseOpponent(msg.Text, msg.From.Username, h.botUsername)
	switch err {
	case nil:
		// fall through
	case duel.ErrNoTarget:
		return h.reply(msg, "Кого вызываем? Укажите соперника: /duel @username")
	case duel.ErrSelfTarget:
		return h.reply(msg, "С самим собой дуэль не выйдет. Вызовите кого-нибудь другого.")
	case duel.ErrBotTarget:
		return h.reply(msg, "Я не дуэлюсь - со мной всё равно не выиграть. Вызовите человека.")
	default:
		h.log.Warn("duel ParseOpponent unexpected error", "error", err)
		return h.reply(msg, "Не удалось разобрать вызов. Используйте: /duel @username")
	}

	bgCtx := context.Background()

	// The opponent must be a real member of THIS chat. The bot only
	// knows users it has observed at least once; without this check
	// "/duel @anyone" (or "/duel @1111111111111111111111111") would
	// announce - and, if the handle belonged to a real account, ping -
	// an arbitrary person who is not even in the chat.
	opMember, err := h.members.GetMemberByUsername(bgCtx, storage.AbsChatID(msg.Chat.ID), op.Username)
	if err != nil {
		// Inert on purpose: a lurker who never wrote is unknown to the
		// bot yet may still be in the chat, so even the rejection must
		// not echo "@handle" (it would notify them).
		return h.reply(msg, fmt.Sprintf(
			"Не знаю участника %s в этом чате. Соперник должен хотя бы раз написать или отреагировать здесь.",
			op.Username))
	}
	if opMember.IsBot {
		return h.reply(msg, "С ботами не дуэлимся. Вызовите человека из чата.")
	}
	if opMember.UserID == msg.From.ID {
		return h.reply(msg, "С самим собой дуэль не выйдет. Вызовите кого-нибудь другого.")
	}

	// Render both sides as INERT text (handle without '@'); a duel is
	// not a sanctioned notifying surface (only the gracekick tag is).
	opponentDisplay := shared.UserDisplay(opMember.Username, opMember.FirstName)
	if opponentDisplay == "" {
		opponentDisplay = fmt.Sprintf("участник %d", opMember.UserID)
	}
	challengerDisplay := shared.UserDisplay(msg.From.Username, msg.From.FirstName)
	if challengerDisplay == "" {
		challengerDisplay = fmt.Sprintf("участник %d", msg.From.ID)
	}

	challengerVal, ok := h.rollOne(bgCtx, msg)
	if !ok {
		return nil // rollOne already replied with the failure notice
	}
	opponentVal, ok := h.rollOne(bgCtx, msg)
	if !ok {
		return nil
	}

	res, err := duel.Decide(challengerVal, opponentVal)
	if err != nil {
		// Telegram returned a value outside 1..6 for a 🎲 - should never
		// happen; both dice were already shown, so just log and stop.
		h.log.Warn("duel Decide failed", "error", err,
			"challenger", challengerVal, "opponent", opponentVal)
		return nil
	}

	var body string
	switch res.Winner {
	case duel.SideChallenger:
		body = fmt.Sprintf("⚔️ Дуэль: %s (%d) против %s (%d).\nПобеждает %s!",
			challengerDisplay, res.ChallengerVal, opponentDisplay, res.OpponentVal, challengerDisplay)
	case duel.SideOpponent:
		body = fmt.Sprintf("⚔️ Дуэль: %s (%d) против %s (%d).\nПобеждает %s!",
			challengerDisplay, res.ChallengerVal, opponentDisplay, res.OpponentVal, opponentDisplay)
	default: // tie
		body = fmt.Sprintf("⚔️ Дуэль: %s (%d) против %s (%d).\nНичья - бросайте ещё: /duel %s",
			challengerDisplay, res.ChallengerVal, opponentDisplay, res.OpponentVal, opponentDisplay)
	}
	return h.replyHTML(msg, body)
}

// rollOne performs one 🎲 SendDice and returns its value. On API failure
// it posts a single friendly notice and returns ok=false so the caller
// aborts without a second roll or an announcement.
func (h *DuelHandler) rollOne(ctx context.Context, msg telego.Message) (int, bool) {
	rolled, err := h.bot.SendDice(ctx, &telego.SendDiceParams{
		ChatID: telego.ChatID{ID: msg.Chat.ID},
		Emoji:  dice.DefaultEmoji,
	})
	if err != nil {
		h.log.Warn("duel SendDice failed", "error", err, "chat_id", msg.Chat.ID)
		_ = h.reply(msg, "Не удалось бросить кубик для дуэли. Повторите позже.")
		return 0, false
	}
	if rolled == nil || rolled.Dice == nil {
		h.log.Warn("duel SendDice returned no dice", "chat_id", msg.Chat.ID)
		_ = h.reply(msg, "Кубик не выпал. Повторите позже.")
		return 0, false
	}
	return rolled.Dice.Value, true
}

func (h *DuelHandler) reply(msg telego.Message, body string) error {
	_, err := h.bot.SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: msg.Chat.ID},
		Text:   body,
		ReplyParameters: &telego.ReplyParameters{
			MessageID: msg.MessageID,
		},
	})
	return err
}

func (h *DuelHandler) replyHTML(msg telego.Message, body string) error {
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
