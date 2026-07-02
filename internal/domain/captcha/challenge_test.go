package captcha

import (
	"fmt"
	"testing"
	"time"
)

func TestGenerateProducesValidChallenge(t *testing.T) {
	t.Parallel()
	const timeout = 10 * time.Minute
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)

	for i := range 500 { // exercise the random paths broadly
		c := Generate(int64(100+i), 200, now, timeout)
		if len(c.ID) != 16 {
			t.Fatalf("ID must be 16 hex chars, got %d: %q", len(c.ID), c.ID)
		}
		if c.UserID != int64(100+i) || c.AbsChatID != 200 {
			t.Fatalf("identity fields not echoed: user=%d chat=%d", c.UserID, c.AbsChatID)
		}
		if !c.CreatedAt.Equal(now) {
			t.Fatalf("CreatedAt must equal now, got %v", c.CreatedAt)
		}
		if want := now.Add(timeout); !c.ExpiresAt.Equal(want) {
			t.Fatalf("ExpiresAt must be now+timeout, got %v want %v", c.ExpiresAt, want)
		}
		if len(c.Answers) != 4 {
			t.Fatalf("must produce exactly 4 answers, got %d (%v)", len(c.Answers), c.Answers)
		}

		var a, b int
		if _, err := fmt.Sscanf(c.Question, "%d + %d", &a, &b); err != nil {
			t.Fatalf("question %q not parseable as 'a + b': %v", c.Question, err)
		}
		if a < 1 || a > 9 || b < 1 || b > 9 {
			t.Fatalf("operands must be 1..9, got %d + %d", a, b)
		}
		if c.CorrectAnswer != a+b {
			t.Fatalf("CorrectAnswer %d != %d + %d = %d", c.CorrectAnswer, a, b, a+b)
		}
		hits := 0
		for _, ans := range c.Answers {
			if ans == c.CorrectAnswer {
				hits++
			}
		}
		if hits != 1 {
			t.Fatalf("correct answer must appear exactly once, got %d (%v)", hits, c.Answers)
		}

		// All answers distinct and non-negative.
		seen := make(map[int]bool, len(c.Answers))
		for _, ans := range c.Answers {
			if ans < 0 {
				t.Fatalf("answer must be >= 0, got %d", ans)
			}
			if seen[ans] {
				t.Fatalf("duplicate answer %d in %v", ans, c.Answers)
			}
			seen[ans] = true
		}
	}
}
