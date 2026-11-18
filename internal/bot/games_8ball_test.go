package bot

import (
	"context"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	"github.com/veschin/bidlobot/internal/testutil"
)

func TestEightBallAnswersOrdinaryQuestionsWithoutReply(t *testing.T) {
	api := testutil.NewMockAPI()
	a := &App{sender: api, cooldown: newCooldown()}
	m := telego.Message{Chat: telego.Chat{ID: -100, Type: telego.ChatTypeSupergroup}, From: &telego.User{ID: 8}, MessageThreadID: 3, MessageID: 15}
	for _, text := range []string{"Всё готово", "/help что?", "Готово?", "И это тоже?"} {
		m.Text = text
		if err := a.maybeEightBall(context.Background(), &m, func(int) int { return 0 }); err != nil {
			t.Fatal(err)
		}
	}
	if len(api.Calls) != 1 {
		t.Fatalf("sent %d messages, want one", len(api.Calls))
	}
	p := api.Calls[0].Params.(*telego.SendMessageParams)
	if p.ReplyParameters != nil || p.MessageThreadID != 3 || p.ChatID.ID != -100 || p.Text == "" {
		t.Fatalf("unexpected standalone answer: %+v", p)
	}
	m.Chat.ID--
	if err := a.maybeEightBall(context.Background(), &m, func(int) int { return 1 }); err != nil || len(api.Calls) != 1 {
		t.Fatalf("miss still sent a message: %v", err)
	}
	future := time.Now().Add(11 * time.Minute)
	a.cooldown.sweepLocked(future)
	if !a.cooldown.last["8ball-auto|-100"].Before(future) || a.cooldown.last["8ball-auto|-100"].IsZero() {
		t.Fatal("an unrelated cooldown sweep cleared the hourly limit")
	}
}
