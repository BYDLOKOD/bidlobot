package summarize

import (
	"strings"
	"testing"
	"time"
)

func TestEstimateTokens_ConservativeForCyrillic(t *testing.T) {
	if got := EstimateTokens(""); got != 0 {
		t.Fatalf("empty = %d, want 0", got)
	}
	// 4 ASCII runes -> (4+1)/2 = 2.
	if got := EstimateTokens("abcd"); got != 2 {
		t.Fatalf("ascii = %d, want 2", got)
	}
	// "привет" is 6 runes (not 12 bytes): the estimate must count runes,
	// otherwise Russian text would be wildly mis-budgeted.
	if got := EstimateTokens("привет"); got != 3 {
		t.Fatalf("cyrillic = %d, want 3 (rune-based)", got)
	}
}

func mkEntries(n int, body string) []Entry {
	out := make([]Entry, n)
	for i := 0; i < n; i++ {
		out[i] = Entry{
			MsgID: i + 1, UserID: int64(i + 1), Name: "user",
			TS:   time.Unix(int64(1700000000+i*60), 0).UTC(),
			Text: body,
		}
	}
	return out
}

func TestBuildPrompt_EmptyWindow(t *testing.T) {
	if _, ok := BuildPrompt(nil, 10, 0, 1000); ok {
		t.Fatalf("empty window must return ok=false")
	}
}

func TestBuildPrompt_AllFitChronological(t *testing.T) {
	entries := mkEntries(5, "hello")
	res, ok := BuildPrompt(entries, 5, 5, 1_000_000)
	if !ok || res.Included != 5 {
		t.Fatalf("included = %d ok=%v, want 5/true", res.Included, ok)
	}
	if !res.From.Equal(entries[0].TS) || !res.To.Equal(entries[4].TS) {
		t.Fatalf("From/To = %v/%v, want %v/%v", res.From, res.To, entries[0].TS, entries[4].TS)
	}
	if len(res.Messages) != 2 || res.Messages[0].Role != "system" || res.Messages[1].Role != "user" {
		t.Fatalf("messages shape wrong: %+v", res.Messages)
	}
	// Oldest line first.
	if !strings.HasPrefix(res.Messages[1].Content, "user [") {
		t.Fatalf("transcript should start with the oldest line: %q", res.Messages[1].Content[:20])
	}
}

func TestBuildPrompt_DropsOldestUnderBudget(t *testing.T) {
	// Each line ~ EstimateTokens("wordwordword")=6 + name 2 + overhead 8.
	entries := mkEntries(50, "wordwordword")
	res, ok := BuildPrompt(entries, 50, 50, EstimateTokens(systemPrompt)+60)
	if !ok || res.Included == 0 || res.Included >= 50 {
		t.Fatalf("included = %d, want a trimmed suffix (0 < n < 50)", res.Included)
	}
	// Kept window must be the most-recent suffix: last TS preserved.
	if !res.To.Equal(entries[49].TS) {
		t.Fatalf("To = %v, want newest %v", res.To, entries[49].TS)
	}
	if res.From.Before(entries[0].TS) {
		t.Fatalf("From earlier than any entry")
	}
}

func TestBuildPrompt_SingleOversizedMessageKept(t *testing.T) {
	entries := mkEntries(3, strings.Repeat("x", 100000))
	res, ok := BuildPrompt(entries, 3, 3, 10) // budget far below one message
	if !ok || res.Included != 1 {
		t.Fatalf("included = %d ok=%v, want exactly the newest 1", res.Included, ok)
	}
	if res.Messages[1].Content == "" {
		t.Fatalf("transcript empty for oversized single message")
	}
}
