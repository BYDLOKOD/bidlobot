package shared

import (
	"strings"
	"testing"
	"time"
)

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1,000"},
		{12847, "12,847"},
		{1000000, "1,000,000"},
	}
	for _, tt := range tests {
		got := FormatNumber(tt.n)
		if got != tt.want {
			t.Errorf("FormatNumber(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestFormatDate(t *testing.T) {
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.UTC)
	if FormatDate(today) != "Today" {
		t.Fatalf("today should be 'Today', got %q", FormatDate(today))
	}

	yesterday := today.Add(-24 * time.Hour)
	s := FormatDate(yesterday)
	if s == "Today" {
		t.Fatal("yesterday should not be 'Today'")
	}
	if s == "" {
		t.Fatal("empty date string")
	}
}


func TestUserDisplay(t *testing.T) {
	// Inert by design: the handle is shown WITHOUT '@' so a stats or
	// game line never becomes a Telegram mention/notification.
	if got := UserDisplay("alice", "Alice"); got != "alice" {
		t.Fatalf("should prefer the handle, inert (no @): got %q", got)
	}
	if UserDisplay("", "Alice") != "Alice" {
		t.Fatal("should fallback to first name")
	}
	if strings.ContainsRune(UserDisplay("alice", "Alice"), '@') {
		t.Fatal("UserDisplay must never emit a literal @-mention")
	}
}

func TestUserDisplayFull(t *testing.T) {
	cases := []struct{ user, first, want string }{
		{"alice", "Alice", "Alice (alice)"},
		{"alice", "", "alice"},
		{"", "Alice", "Alice"},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := UserDisplayFull(c.user, c.first); got != c.want {
			t.Errorf("UserDisplayFull(%q,%q)=%q want %q", c.user, c.first, got, c.want)
		}
	}
	if strings.ContainsRune(UserDisplayFull("alice", "Alice"), '@') {
		t.Fatal("UserDisplayFull must never emit a literal @-mention")
	}
}

func TestTodayUTC(t *testing.T) {
	today := TodayUTC()
	if today.Hour() != 0 || today.Minute() != 0 || today.Second() != 0 {
		t.Fatal("TodayUTC should be midnight")
	}
}
