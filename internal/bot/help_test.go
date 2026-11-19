package bot

import (
	"strings"
	"testing"
)

// TestBuildHelpMarkdownContainsSections verifies the reference lists the
// core commands with call examples, in code spans, and that the owner block
// appears only for the owner.
func TestBuildHelpMarkdownContainsSections(t *testing.T) {
	text := buildHelpMarkdown(false)
	for _, want := range []string{
		"*BidloBot \\- команды*",
		"`/stats top` \\- топ участников",
		"`/battle Go Rust` \\- голосование реакциями за 60 секунд",
		"`/praise @user` \\- похвалить участника",
		"`/refreport ID` \\- удалить рефку по ID",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("help missing %q", want)
		}
	}
	if strings.Contains(text, "`/chats`") {
		t.Error("owner-only /chats leaked into non-owner help")
	}
	if !strings.Contains(buildHelpMarkdown(true), "`/chats`") {
		t.Error("owner help must include /chats")
	}
}

// TestHelpMarkdownV2Escaping verifies the rendered text is safe for
// Telegram MarkdownV2: special characters outside a code span must be
// backslash-escaped (except `*`, which is the bold marker and must appear
// in balanced pairs), and code spans must be balanced. A stray unescaped
// `.` or `!` would make Telegram reject the whole message with 400.
func TestHelpMarkdownV2Escaping(t *testing.T) {
	const special = "_[]()~`>#+-=|{}.!"
	for _, owner := range []bool{false, true} {
		text := buildHelpMarkdown(owner)
		inCode := false
		stars := 0
		for i := range text {
			c := text[i]
			if c == '`' {
				inCode = !inCode
				continue
			}
			if inCode {
				continue
			}
			switch {
			case c == '*':
				// Bold marker: must form a pair (start+end). Escaped
				// literal stars are not allowed in this text.
				if i > 0 && text[i-1] == '\\' {
					t.Fatalf("owner=%v: escaped literal star at offset %d", owner, i)
				}
				stars++
			case strings.ContainsRune(special, rune(c)):
				if i == 0 || text[i-1] != '\\' {
					t.Fatalf("owner=%v: unescaped MarkdownV2 char %q at offset %d", owner, string(c), i)
				}
			}
		}
		if inCode {
			t.Fatalf("owner=%v: unbalanced code span", owner)
		}
		if stars%2 != 0 {
			t.Fatalf("owner=%v: odd number of bold markers (%d)", owner, stars)
		}
	}
}
