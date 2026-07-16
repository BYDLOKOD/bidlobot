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
	if _, ok := BuildPrompt(nil, 10, 0, 1000, ""); ok {
		t.Fatalf("empty window must return ok=false")
	}
}

func TestBuildPrompt_AllFitChronological(t *testing.T) {
	entries := mkEntries(5, "hello")
	res, ok := BuildPrompt(entries, 5, 5, 1_000_000, "")
	if !ok || res.Included != 5 {
		t.Fatalf("included = %d ok=%v, want 5/true", res.Included, ok)
	}
	if !res.From.Equal(entries[0].TS) || !res.To.Equal(entries[4].TS) {
		t.Fatalf("From/To = %v/%v, want %v/%v", res.From, res.To, entries[0].TS, entries[4].TS)
	}
	if res.SystemPrompt == "" {
		t.Fatalf("system prompt empty")
	}
	// Transcript declares the retained count, then presents oldest first.
	if !strings.HasPrefix(res.Transcript, "Transcript message count: 5\nuser [") {
		t.Fatalf("transcript header or chronological first line missing: %q", res.Transcript[:50])
	}
}

func TestBuildPrompt_DropsOldestUnderBudget(t *testing.T) {
	// Each line ~ EstimateTokens("wordwordword")=6 + name 2 + overhead 8.
	entries := mkEntries(50, "wordwordword")
	res, ok := BuildPrompt(entries, 50, 50, EstimateTokens(systemPrompt)+60, "")
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
	res, ok := BuildPrompt(entries, 3, 3, 10, "") // budget far below one message
	if !ok || res.Included != 1 {
		t.Fatalf("included = %d ok=%v, want exactly the newest 1", res.Included, ok)
	}
	if res.Transcript == "" {
		t.Fatalf("transcript empty for oversized single message")
	}
}

func TestBuildPrompt_QuestionsAppended(t *testing.T) {
	entries := mkEntries(3, "hello")
	res, ok := BuildPrompt(entries, 3, 3, 1_000_000, "что решили по деплою?")
	if !ok {
		t.Fatalf("ok=false with questions")
	}
	body := res.Transcript
	if !strings.Contains(body, "---") {
		t.Fatalf("questions separator not found in transcript")
	}
	if !strings.Contains(body, "что решили по деплою?") {
		t.Fatalf("questions text not found in transcript")
	}
}

func TestBuildPrompt_EmptyQuestionsNoSeparator(t *testing.T) {
	entries := mkEntries(3, "hello")
	res, ok := BuildPrompt(entries, 3, 3, 1_000_000, "")
	if !ok {
		t.Fatalf("ok=false")
	}
	if strings.Contains(res.Transcript, "---") {
		t.Fatalf("empty questions must not add separator")
	}
}

func TestBuildPrompt_RelevanceWeightedInstructions(t *testing.T) {
	entries := mkEntries(1, "hello")
	res, ok := BuildPrompt(entries, 1, 1, 1_000_000, "")
	if !ok {
		t.Fatalf("ok=false")
	}
	for _, want := range []string{
		"Deliberate omission is required",
		"roughly in proportion",
		"below roughly 5%",
		"Never mention a discarded thread",
		"A topic label is not a summary",
		"Never produce a link catalog",
	} {
		if !strings.Contains(res.SystemPrompt, want) {
			t.Fatalf("system prompt missing relevance rule %q", want)
		}
	}
	if !strings.HasPrefix(res.Transcript, "Transcript message count: 1\n") {
		t.Fatalf("transcript must declare retained message count: %q", res.Transcript)
	}
}
