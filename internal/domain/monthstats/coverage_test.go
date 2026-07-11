package monthstats

import (
	"testing"

	"github.com/mymmrac/telego"
)

// TestHasContentIncludesDice verifies that Telegram native dice messages
// (msg.Dice != nil) count as content. The current HasContent does not
// check msg.Dice, so a standalone dice throw without text is invisible to
// monthly statistics.
func TestHasContentIncludesDice(t *testing.T) {
	msg := &telego.Message{
		Text: "",
		Dice: &telego.Dice{Emoji: "🎲", Value: 5},
	}
	msg.From = &telego.User{ID: 42, IsBot: false}

	if !HasContent(msg) {
		t.Fatal("HasContent must return true for a native dice message (msg.Dice != nil)")
	}
}

// TestHasContentExcludesSlashCommands verifies that slash-command messages
// (text starting with "/") are excluded from content counting. A user
// asking the bot for /stats must not inflate their own message count.
func TestHasContentExcludesSlashCommands(t *testing.T) {
	msg := &telego.Message{
		Text: "/stats today",
	}
	msg.From = &telego.User{ID: 42, IsBot: false}

	if HasContent(msg) {
		t.Fatal("HasContent must return false for a slash command message")
	}
}

// TestHasContentExcludesBotCommands ensures any bot command prefix is
// excluded, not just /stats.
func TestHasContentExcludesBotCommands(t *testing.T) {
	cases := []string{
		"/praise @someone",
		"/roast 42",
		"/8ball стоит ли?",
		"/help",
		"/start",
	}
	for _, tc := range cases {
		msg := &telego.Message{Text: tc}
		msg.From = &telego.User{ID: 1, IsBot: false}
		if HasContent(msg) {
			t.Fatalf("HasContent must exclude /command messages, got true for %q", tc)
		}
	}
}
