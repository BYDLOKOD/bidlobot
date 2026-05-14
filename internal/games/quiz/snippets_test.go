package quiz

import (
	"math/rand"
	"sort"
	"testing"
)

func TestSnippetCountAtLeastTen(t *testing.T) {
	if SnippetCount() < 10 {
		t.Errorf("expected at least 10 snippets, got %d", SnippetCount())
	}
}

func TestEverySnippetAnswerIsKnown(t *testing.T) {
	known := make(map[Lang]bool)
	for _, l := range AllLangs {
		known[l] = true
	}
	for i := 0; i < SnippetCount(); i++ {
		s, _ := GetSnippet(i)
		if !known[s.Answer] {
			t.Errorf("snippet %d has unknown answer %v", i, s.Answer)
		}
	}
}

func TestLanguageMixCoversEveryLanguage(t *testing.T) {
	mix := LanguageMix()
	covered := make(map[Lang]bool)
	for _, m := range mix {
		covered[m.Lang] = true
		if m.Count == 0 {
			t.Errorf("LanguageMix returned zero count for %v", m.Lang)
		}
	}
	for _, l := range AllLangs {
		if !covered[l] {
			t.Errorf("language %v has no snippets in the pool", l)
		}
	}
}

func TestGetSnippetOutOfRange(t *testing.T) {
	if _, err := GetSnippet(-1); err == nil {
		t.Error("expected error for -1")
	}
	if _, err := GetSnippet(SnippetCount()); err == nil {
		t.Error("expected error for SnippetCount()")
	}
}

func TestPickRandomInRange(t *testing.T) {
	r := rand.New(rand.NewSource(42))
	for i := 0; i < 200; i++ {
		idx := PickRandom(r)
		if idx < 0 || idx >= SnippetCount() {
			t.Fatalf("PickRandom returned %d (out of range)", idx)
		}
	}
}

func TestBuildOptionsIncludesAnswer(t *testing.T) {
	r := rand.New(rand.NewSource(42))
	for i := 0; i < SnippetCount(); i++ {
		s, _ := GetSnippet(i)
		opts, correctIdx, err := BuildOptions(i, r)
		if err != nil {
			t.Fatalf("snippet %d: %v", i, err)
		}
		if len(opts) != 4 {
			t.Fatalf("snippet %d: expected 4 options, got %d", i, len(opts))
		}
		if opts[correctIdx] != s.Answer {
			t.Errorf("snippet %d: correctIdx %d points to %v, expected answer %v",
				i, correctIdx, opts[correctIdx], s.Answer)
		}
	}
}

func TestBuildOptionsAreUnique(t *testing.T) {
	r := rand.New(rand.NewSource(42))
	for i := 0; i < SnippetCount(); i++ {
		opts, _, err := BuildOptions(i, r)
		if err != nil {
			t.Fatal(err)
		}
		seen := make(map[Lang]bool)
		for _, o := range opts {
			if seen[o] {
				t.Errorf("snippet %d: duplicate option %v", i, o)
			}
			seen[o] = true
		}
	}
}

func TestBuildOptionsCorrectIdxValid(t *testing.T) {
	r := rand.New(rand.NewSource(42))
	for i := 0; i < SnippetCount(); i++ {
		_, idx, err := BuildOptions(i, r)
		if err != nil {
			t.Fatal(err)
		}
		if idx < 0 || idx > 3 {
			t.Errorf("snippet %d: correctIdx %d out of 0..3", i, idx)
		}
	}
}

func TestBuildOptionsBadSnippet(t *testing.T) {
	if _, _, err := BuildOptions(-1, nil); err == nil {
		t.Error("expected error for bad snippet idx")
	}
}

func TestLangTitleAllSet(t *testing.T) {
	for _, l := range AllLangs {
		if l.Title() == "?" {
			t.Errorf("Lang %d has placeholder title", l)
		}
	}
}

func TestLangIDsAreContiguous(t *testing.T) {
	// Sanity check that nobody accidentally added a Lang with a
	// non-contiguous value, which would make callback_data parsing
	// brittle if someone reorders them later.
	ids := make([]int, 0, len(AllLangs))
	for _, l := range AllLangs {
		ids = append(ids, int(l))
	}
	sort.Ints(ids)
	for i, id := range ids {
		if id != i {
			t.Errorf("Lang IDs not contiguous: position %d has value %d", i, id)
		}
	}
}
