package bot

import (
	"testing"

	"github.com/mymmrac/telego"
	"github.com/veschin/bidlobot/internal/testutil"
)

func TestFarmCommandOpensTheAppWithoutJoining(t *testing.T) {
	api := &excuseTestAPI{MockAPI: testutil.NewMockAPI(), user: telego.User{ID: 42}}
	g := &gameActions{api: api, log: testLogger()}
	msg := telego.Message{Chat: telego.Chat{ID: -100}, From: &api.user, MessageThreadID: 7, Text: "/farm@bidlo_reborn_bot"}
	if err := g.command("farm")(nil, msg); err != nil {
		t.Fatal(err)
	}
	if got := api.Messages[0]; got.Text != "Карта фермы пока недоступна." || got.Keyboard != nil {
		t.Fatalf("unconfigured farm advertised a game: %+v", got)
	}
	const link = "https://t.me/bot/farm?startapp=fixture"
	g.farmLink = func(chatID int64) string {
		if chatID != msg.Chat.ID {
			t.Fatalf("farm chat = %d, want %d", chatID, msg.Chat.ID)
		}
		return link
	}
	if err := g.command("farm")(nil, msg); err != nil {
		t.Fatal(err)
	}
	p := api.Calls[len(api.Calls)-1].Params.(*telego.SendMessageParams)
	keyboard, ok := p.ReplyMarkup.(*telego.InlineKeyboardMarkup)
	if !ok || p.MessageThreadID != 7 || len(keyboard.InlineKeyboard) != 1 || keyboard.InlineKeyboard[0][0].URL != link {
		t.Fatalf("bad farm entry: %+v", p)
	}
}
