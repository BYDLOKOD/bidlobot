package shared

import (
	"fmt"
	"strings"
	"time"

	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

var printer = message.NewPrinter(language.English)

func FormatNumber(n int64) string {
	return printer.Sprintf("%d", n)
}

func FormatDate(t time.Time) string {
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if t.UTC().After(today) || t.UTC().Equal(today) {
		return "Today"
	}
	return t.UTC().Format("Jan 2, 2006")
}

func EscapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// UserDisplay renders a member's identity as INERT text: the handle is
// shown WITHOUT a leading '@'. A literal "@handle" of a real account is
// parsed by Telegram as a mention that notifies that user, so a command
// anyone can run (stats, games, attribution lines) would ping everyone
// it lists every time it is invoked - reading a leaderboard would
// mass-summon the chat. Bare "handle" is plain, non-notifying text.
// The ONLY sanctioned member-notifying output is the gracekick public
// tag, which builds its own mention() and never routes through here.
func UserDisplay(username, firstName string) string {
	if username != "" {
		return username
	}
	return EscapeHTML(firstName)
}

// UserDisplayFull shows BOTH the human name and the handle when known:
// "Имя (handle)" - the handle WITHOUT '@', for the same anti-mention
// reason as UserDisplay (do not turn a stats/games line into a
// notification). Falls back to "handle" (no name) or the escaped name
// (no handle - e.g. history imported from a Telegram Desktop export,
// which carries display names but no usernames; the handle fills in
// once that user writes live). Returns "" when nothing is known so
// callers fall back to "User <id>". Output is HTML-safe (name escaped;
// handle chars are [A-Za-z0-9_]); callers must NOT re-escape it.
func UserDisplayFull(username, firstName string) string {
	name := EscapeHTML(strings.TrimSpace(firstName))
	switch {
	case name != "" && username != "":
		return fmt.Sprintf("%s (%s)", name, username)
	case username != "":
		return username
	default:
		return name
	}
}

func TodayUTC() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}
