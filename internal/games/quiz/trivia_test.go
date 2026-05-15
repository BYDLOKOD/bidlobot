package quiz

import (
	"math/rand"
	"testing"
)

func TestTriviaPoolHealthy(t *testing.T) {
	if TriviaCount() < 25 {
		t.Errorf("trivia pool too small: %d (want >= 25)", TriviaCount())
	}
	for i := 0; i < TriviaCount(); i++ {
		tr, err := GetTrivia(i)
		if err != nil {
			t.Fatalf("GetTrivia(%d): %v", i, err)
		}
		if tr.Question == "" {
			t.Errorf("question %d has empty text", i)
		}
		if tr.CorrectIdx < 0 || tr.CorrectIdx > 3 {
			t.Errorf("question %d has CorrectIdx %d outside 0..3", i, tr.CorrectIdx)
		}
		seen := map[string]bool{}
		for j, o := range tr.Options {
			if o == "" {
				t.Errorf("question %d option %d empty", i, j)
			}
			if seen[o] {
				t.Errorf("question %d has duplicate option %q", i, o)
			}
			seen[o] = true
		}
	}
}

func TestGetTriviaOutOfRange(t *testing.T) {
	if _, err := GetTrivia(-1); err != ErrTriviaIndex {
		t.Errorf("negative index should be ErrTriviaIndex, got %v", err)
	}
	if _, err := GetTrivia(TriviaCount()); err != ErrTriviaIndex {
		t.Errorf("index == count should be ErrTriviaIndex, got %v", err)
	}
}

func TestPickRandomTriviaInRange(t *testing.T) {
	r := rand.New(rand.NewSource(42))
	for i := 0; i < 200; i++ {
		idx := PickRandomTrivia(r)
		if idx < 0 || idx >= TriviaCount() {
			t.Fatalf("PickRandomTrivia returned out-of-range %d", idx)
		}
	}
}

func TestBuildTriviaOptionsCorrectIdxTracksAnswer(t *testing.T) {
	for seed := int64(0); seed < 20; seed++ {
		for idx := 0; idx < TriviaCount(); idx++ {
			r := rand.New(rand.NewSource(seed))
			labels, correctIdx, err := BuildTriviaOptions(idx, r)
			if err != nil {
				t.Fatalf("BuildTriviaOptions(%d) seed %d: %v", idx, seed, err)
			}
			if len(labels) != 4 {
				t.Fatalf("expected 4 labels, got %d", len(labels))
			}
			tr, _ := GetTrivia(idx)
			want := tr.Options[tr.CorrectIdx]
			if labels[correctIdx] != want {
				t.Errorf("idx %d seed %d: correctIdx %d points to %q, want %q",
					idx, seed, correctIdx, labels[correctIdx], want)
			}
			// All four canonical options must still be present.
			present := map[string]bool{}
			for _, l := range labels {
				present[l] = true
			}
			for _, o := range tr.Options {
				if !present[o] {
					t.Errorf("idx %d seed %d: shuffled labels lost option %q", idx, seed, o)
				}
			}
		}
	}
}

func TestBuildTriviaOptionsBadIndex(t *testing.T) {
	if _, _, err := BuildTriviaOptions(-1, rand.New(rand.NewSource(1))); err != ErrTriviaIndex {
		t.Errorf("bad index should be ErrTriviaIndex, got %v", err)
	}
}

// Snippet API must remain intact alongside the new trivia data.
func TestSnippetAPIUnaffectedByTrivia(t *testing.T) {
	if SnippetCount() < 4 {
		t.Errorf("snippet pool unexpectedly small: %d", SnippetCount())
	}
	if _, err := GetSnippet(0); err != nil {
		t.Errorf("GetSnippet(0) should still work: %v", err)
	}
}
