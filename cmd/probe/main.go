// Command probe checks that the configured TG_BOT_TOKEN authenticates
// and prints the bot's identity. No polling, no message sending - safe
// to run against a live chat.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/mymmrac/telego"
)

func main() {
	token := os.Getenv("TG_BOT_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "TG_BOT_TOKEN is required")
		os.Exit(1)
	}
	bot, err := telego.NewBot(token)
	if err != nil {
		fmt.Fprintln(os.Stderr, "create bot:", err)
		os.Exit(1)
	}
	me, err := bot.GetMe(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "GetMe:", err)
		os.Exit(1)
	}
	fmt.Printf("authenticated: @%s (id=%d, can_join=%v, can_read_all=%v, supports_inline=%v)\n",
		me.Username, me.ID, me.CanJoinGroups, me.CanReadAllGroupMessages, me.SupportsInlineQueries)
}
