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

func UserDisplay(username, firstName string) string {
	if username != "" {
		return fmt.Sprintf("@%s", username)
	}
	return EscapeHTML(firstName)
}

// UserDisplayFull shows BOTH the human name and the @handle when known:
// "Имя (@handle)". Falls back to "@handle" (no name) or the escaped name
// (no handle - e.g. history imported from a Telegram Desktop export,
// which carries display names but no usernames; the @handle fills in
// automatically once that user writes live). Returns "" when nothing is
// known so callers can fall back to "User <id>". Output is HTML-safe
// (name escaped; @handle chars are [A-Za-z0-9_]); callers must NOT
// re-escape it.
func UserDisplayFull(username, firstName string) string {
	name := EscapeHTML(strings.TrimSpace(firstName))
	switch {
	case name != "" && username != "":
		return fmt.Sprintf("%s (@%s)", name, username)
	case username != "":
		return fmt.Sprintf("@%s", username)
	default:
		return name
	}
}

func TodayUTC() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}
