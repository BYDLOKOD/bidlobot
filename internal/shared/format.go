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

func TodayUTC() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}
