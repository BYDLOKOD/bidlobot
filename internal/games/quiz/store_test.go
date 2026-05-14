package quiz

import (
	"testing"
	"time"
)

func TestActiveQuizzesRegisterAndGet(t *testing.T) {
	a := NewActiveQuizzes()
	q := &ActiveQuiz{MessageID: 100, AbsChatID: 1, SnippetIdx: 0, CorrectIdx: 1, Options: []Lang{LangPython, LangGo, LangJS, LangRust}}
	a.Register(q)
	got := a.Get(100)
	if got != q {
		t.Errorf("Get must return the registered quiz")
	}
	if a.Active() != 1 {
		t.Errorf("expected 1 active, got %d", a.Active())
	}
}

func TestActiveQuizzesRegisterIgnoresZeroOrNil(t *testing.T) {
	a := NewActiveQuizzes()
	a.Register(nil)
	a.Register(&ActiveQuiz{MessageID: 0})
	if a.Active() != 0 {
		t.Errorf("zero/nil quizzes must be ignored, got %d active", a.Active())
	}
}

func TestActiveQuizzesGetMissing(t *testing.T) {
	a := NewActiveQuizzes()
	if a.Get(99) != nil {
		t.Error("expected nil for missing quiz")
	}
}

func TestActiveQuizzesMarkSolvedFirstWins(t *testing.T) {
	a := NewActiveQuizzes()
	a.Register(&ActiveQuiz{MessageID: 100, AbsChatID: 1, StartedAt: time.Now()})
	if !a.MarkSolved(100, 200, "@alice") {
		t.Error("first solver should win")
	}
	if a.MarkSolved(100, 300, "@bob") {
		t.Error("second solver should lose")
	}
	q := a.Get(100)
	if q.WinnerID != 200 || q.WinnerName != "@alice" {
		t.Errorf("winner should be alice, got id=%d name=%q", q.WinnerID, q.WinnerName)
	}
}

func TestActiveQuizzesMarkSolvedMissing(t *testing.T) {
	a := NewActiveQuizzes()
	if a.MarkSolved(99, 200, "x") {
		t.Error("MarkSolved on unknown msg must return false")
	}
}

func TestActiveQuizzesForget(t *testing.T) {
	a := NewActiveQuizzes()
	a.Register(&ActiveQuiz{MessageID: 100})
	a.Forget(100)
	if a.Get(100) != nil {
		t.Error("Forget should remove the quiz")
	}
	if a.Active() != 0 {
		t.Errorf("expected 0 active after Forget, got %d", a.Active())
	}
	// Forget on missing ID is a no-op
	a.Forget(99)
}
