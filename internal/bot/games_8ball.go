package bot

import (
	"context"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

const eightBallQuiet = time.Hour

var eightBallAnswers = [...]string{
	"Да, но сначала прогони тесты.",
	"Да. Спроси у сеньора, он подтвердит.",
	"Да, как только закроешь техдолг.",
	"Да. Только не в пятницу.",
	"Да. Но сначала сделай копию.",
	"Вот теперь можно.",
	"Спроси позже, сейчас идёт миграция.",
	"Спроси у того, кто писал этот код. Если найдёшь его.",
	"Ответ в логах. Если они есть.",
	"Зависит от того, кого разбудят первым.",
	"Я бы подождал до утра.",
	"Нет. Откатывай.",
	"Нет. Это уже ломали, не повторяй.",
	"Нет. Линтер против, и он прав.",
	"Не сегодня. Дежурный уже спит.",
	"Лучше не надо. Там гонка по данным.",
}

func (a *App) handleEightBall(h *th.Context, msg telego.Message) error {
	if msg.From == nil || msg.From.IsBot || msg.SenderChat != nil {
		return nil
	}
	text := "Напиши вопрос после /8ball."
	if commandArgs(msg.Text) != "" {
		text = eightBallAnswers[rand.IntN(len(eightBallAnswers))]
	}
	return a.sendEightBall(h.Context(), &msg, text, &telego.ReplyParameters{MessageID: msg.MessageID})
}

func (a *App) eightBallMiddleware(h *th.Context, update telego.Update) error {
	if err := a.maybeEightBall(h.Context(), update.Message, rand.IntN); err != nil {
		a.log.Warn("send spontaneous 8ball", "error", err)
	}
	return h.Next(update)
}

func (a *App) maybeEightBall(ctx context.Context, msg *telego.Message, pick func(int) int) error {
	if msg == nil || msg.Chat.Type != telego.ChatTypeSupergroup || msg.From == nil || msg.From.IsBot || msg.SenderChat != nil {
		return nil
	}
	text := strings.TrimSpace(msg.Text)
	if strings.HasPrefix(text, "/") || !strings.Contains(text, "?") || pick(200) != 0 {
		return nil
	}
	if allowed, _ := a.cooldown.gate(msg.Chat.ID, "8ball-auto", eightBallQuiet); !allowed {
		return nil
	}
	return a.sendEightBall(ctx, msg, eightBallAnswers[pick(len(eightBallAnswers))], nil)
}

func (a *App) sendEightBall(ctx context.Context, msg *telego.Message, text string, reply *telego.ReplyParameters) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := a.sender.SendMessage(ctx, &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: msg.Chat.ID}, MessageThreadID: msg.MessageThreadID,
		Text: text, ReplyParameters: reply,
	})
	return err
}
