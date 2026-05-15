package monthstats

import (
	"strings"
	"testing"

	"github.com/mymmrac/telego"
)

// anonAdminID is Telegram's GroupAnonymousBot user id; shared.IsAnonymousAdmin
// keys off it. Hard-coded here so the exclusion test is explicit.
const anonAdminID int64 = 1087968824

func TestRuneLen(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"", 0},
		{"Благодарю", 9},   // chat-export.org: :text-length 9
		{"abc", 3},         //
		{"привет мир", 10}, // cyrillic counted as code points
		{"a😀b", 3},         // astral char = 1 rune (Go), documented divergence
		{strings.Repeat("я", 50), 50},
	}
	for _, c := range cases {
		if got := RuneLen(c.in); got != c.want {
			t.Errorf("RuneLen(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestAddEntityType(t *testing.T) {
	var s Sample
	for _, typ := range []string{
		"custom_emoji", "custom_emoji", "code", "mention", "mention", "mention",
		"bot_command", "pre", "mention_name", "bold", "url", "", "unknown",
	} {
		s.AddEntityType(typ)
	}
	// Only the four legacy nominations are tracked; pre/mention_name/bold/url
	// are deliberately NOT counted (legacy parity).
	if s.CustomEmoji != 2 || s.Code != 1 || s.Mention != 3 || s.BotCommand != 1 {
		t.Fatalf("entity tally wrong: %+v", s)
	}
}

func TestCountKeyword(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"ничего полезного", 0},
		{"люблю курсор", 1},
		{"Cursor и КУРСОР и cursor", 3}, // case-insensitive, default pattern
		{"курсором пишу в Cursor", 2},   // substring matches, mirrors legacy re-seq
	}
	for _, c := range cases {
		if got := CountKeyword(c.in); got != c.want {
			t.Errorf("CountKeyword(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestExcerpt(t *testing.T) {
	short := "короткое"
	if txt, full := Excerpt(short); txt != short || !full {
		t.Fatalf("short excerpt: got %q full=%v", txt, full)
	}
	long := strings.Repeat("я", LongestExcerptRunes+50)
	txt, full := Excerpt(long)
	if full {
		t.Fatal("long excerpt should report full=false")
	}
	if RuneLen(txt) != int64(LongestExcerptRunes) {
		t.Fatalf("long excerpt rune len = %d, want %d", RuneLen(txt), LongestExcerptRunes)
	}
}

func msg(mod func(*telego.Message)) *telego.Message {
	m := &telego.Message{
		MessageID: 1,
		Date:      1754341320, // 2025-08-05T00:02:00Z
		From:      &telego.User{ID: 42, FirstName: "Олег"},
		Chat:      telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
		Text:      "Благодарю",
	}
	if mod != nil {
		mod(m)
	}
	return m
}

func TestExtractSampleExclusions(t *testing.T) {
	cases := []struct {
		name string
		mod  func(*telego.Message)
	}{
		{"bot sender", func(m *telego.Message) { m.From.IsBot = true }},
		{"anonymous admin", func(m *telego.Message) { m.From.ID = anonAdminID }},
		{"sender chat", func(m *telego.Message) { m.SenderChat = &telego.Chat{ID: -100} }},
		{"nil from", func(m *telego.Message) { m.From = nil }},
		{"no content", func(m *telego.Message) { m.Text = "" }},
	}
	for _, c := range cases {
		if _, ok := ExtractSample(msg(c.mod)); ok {
			t.Errorf("%s: expected excluded (ok=false)", c.name)
		}
	}
}

func TestExtractSampleHappyPath(t *testing.T) {
	m := msg(func(m *telego.Message) {
		m.Text = "смотри код и курсор"
		m.Entities = []telego.MessageEntity{{Type: "code"}, {Type: "mention"}}
		m.Caption = "подпись cursor"
		m.CaptionEntities = []telego.MessageEntity{{Type: "custom_emoji"}}
	})
	s, ok := ExtractSample(m)
	if !ok {
		t.Fatal("expected included")
	}
	if s.AbsChatID != 1001234567890 {
		t.Errorf("AbsChatID = %d, want positive abs form", s.AbsChatID)
	}
	if s.UserID != 42 {
		t.Errorf("UserID = %d", s.UserID)
	}
	if s.Month != "2025-08" {
		t.Errorf("Month = %q, want 2025-08", s.Month)
	}
	if want := RuneLen("смотри код и курсор") + RuneLen("подпись cursor"); s.Runes != want {
		t.Errorf("Runes = %d, want %d (text+caption)", s.Runes, want)
	}
	if s.Code != 1 || s.Mention != 1 || s.CustomEmoji != 1 {
		t.Errorf("entities from Entities+CaptionEntities wrong: %+v", s)
	}
	if s.Keyword != 2 { // "курсор" in text + "cursor" in caption
		t.Errorf("Keyword = %d, want 2 (text+caption)", s.Keyword)
	}
}

func TestSetKeywordPattern(t *testing.T) {
	t.Cleanup(func() { _ = SetKeywordPattern(DefaultKeywordPattern) })
	if err := SetKeywordPattern("(?i)golang|go"); err != nil {
		t.Fatal(err)
	}
	if CountKeyword("я пишу на Golang и go") != 2 {
		t.Fatal("custom keyword pattern not applied")
	}
	if err := SetKeywordPattern("("); err == nil {
		t.Fatal("expected invalid regex to error")
	}
	// Invalid pattern must leave the previous regex in force.
	if CountKeyword("golang") != 1 {
		t.Fatal("invalid SetKeywordPattern must not clobber the working regex")
	}
}
