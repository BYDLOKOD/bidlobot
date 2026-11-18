package bot

import (
	"slices"
	"strings"
	"testing"
)

// The positive catalogue is exactly the launch set: 16 generic lines,
// 8 help, 8 explanation and 8 review lines. Reason families stay
// disjoint so a generic praise can never surface a reason-specific line.
func TestPraiseCatalogueShape(t *testing.T) {
	cases := []struct {
		name string
		pool []string
		want int
	}{
		{"generic", praiseGeneric, 16},
		{"help", praiseHelp, 8},
		{"explanation", praiseExplanation, 8},
		{"review", praiseReview, 8},
	}
	seen := make(map[string]string)
	total := 0
	for _, c := range cases {
		if len(c.pool) != c.want {
			t.Errorf("%s pool = %d, want %d", c.name, len(c.pool), c.want)
		}
		for i, line := range c.pool {
			if strings.TrimSpace(line) == "" {
				t.Errorf("%s line %d is empty", c.name, i+1)
			}
			if strings.Contains(line, "%s") {
				t.Errorf("%s line %d must be a final sentence, got placeholder %q", c.name, i+1, line)
			}
			if other, dup := seen[line]; dup {
				t.Errorf("line %q appears in both %s and %s", line, other, c.name)
			}
			seen[line] = c.name
			total++
		}
	}
	if total != 40 {
		t.Fatalf("catalogue has %d lines, want 40", total)
	}
}

func TestRepCopySelectionAvoidsLastFive(t *testing.T) {
	repCopy.reset()
	defer repCopy.reset()
	const chatID int64 = -100

	for _, reason := range []repReason{repReasonNone, repReasonHelp, repReasonExplanation, repReasonReview} {
		lines := praiseCatalogue(reason)
		var recent []string
		for range 25 {
			line := praiseLine(chatID, reason)
			if !slices.Contains(lines, line) {
				t.Fatalf("%s selected %q outside its catalogue", reason.family(), line)
			}
			for _, prev := range recent {
				if line == prev {
					t.Fatalf("%s repeated %q within the last five uses: %v", reason.family(), line, recent)
				}
			}
			recent = append(recent, line)
			if len(recent) > 5 {
				recent = recent[1:]
			}
		}
	}
}

func TestRepCopyHistoryIsPerChatAndFamily(t *testing.T) {
	repCopy.reset()
	defer repCopy.reset()

	const chatA, chatB int64 = -100, -200
	for range 3 {
		_ = praiseLine(chatA, repReasonNone)
	}
	keyA := repCopyFamily{chatID: chatA, name: repReasonNone.family()}
	keyB := repCopyFamily{chatID: chatB, name: repReasonNone.family()}
	if got := len(repCopy.last[keyA]); got != 3 {
		t.Fatalf("chat A history length = %d, want 3", got)
	}
	if _, ok := repCopy.last[keyB]; ok {
		t.Fatal("chat B must not inherit chat A's history")
	}
	_ = praiseLine(chatB, repReasonNone)
	if _, ok := repCopy.last[keyB]; !ok {
		t.Fatal("chat B must get its own history entry")
	}
}

func TestParseRepReason(t *testing.T) {
	cases := []struct {
		args     string
		want     repReason
		wantRest string
	}{
		{"@bob", repReasonNone, "@bob"},
		{"help @bob", repReasonHelp, "@bob"},
		{"explanation @bob", repReasonExplanation, "@bob"},
		{"review @bob", repReasonReview, "@bob"},
		{"HELP @bob", repReasonHelp, "@bob"},
		{"помощь @bob", repReasonHelp, "@bob"},
		{"", repReasonNone, ""},
		{"help", repReasonHelp, ""},
		{"@help", repReasonNone, "@help"},
		{"bob help", repReasonNone, "bob help"},
	}
	for _, c := range cases {
		got, rest := parseRepReason(c.args)
		if got != c.want || rest != c.wantRest {
			t.Errorf("parseRepReason(%q) = %v, %q; want %v, %q", c.args, got, rest, c.want, c.wantRest)
		}
	}
}
