package bot

import (
	"testing"
)

// TestClassifyQuestionClosedCues verifies that questions containing the
// standalone Russian word "ли", "да или нет", or known closed-question cues
// ("можно", "нужно", "стоит", "будет", "получится", "смогу", "сможет",
// "успею", "успеет", "есть", "правда") are classified as closed.
//
// This test will not compile because ClassifyQuestion does not exist yet.
func TestClassifyQuestionClosedCues(t *testing.T) {
	closed := []string{
		"Мне стоит это сделать?",
		"Получится ли?",
		"Можно ли это починить?",
		"Нужно ли мне идти?",
		"да или нет?",
		"Будет ли дождь?",
		"Правда ли это?",
	}
	for _, q := range closed {
		cls := ClassifyQuestion(q)
		if cls != "closed" {
			t.Fatalf("expected closed for %q, got %q", q, cls)
		}
	}
}

// TestClassifyQuestionOpenCues verifies that questions without closed cues
// are classified as open. Only "ли", "да или нет", and the closed cue list
// trigger "closed"; everything else is "open".
//
// This test will not compile because ClassifyQuestion does not exist yet.
func TestClassifyQuestionOpenCues(t *testing.T) {
	open := []string{
		"Что делать?",
		"Как это работает?",
		"Почему небо голубое?",
		"Где мой кофе?",
		"Кто это сделал?",
	}
	for _, q := range open {
		cls := ClassifyQuestion(q)
		if cls != "open" {
			t.Fatalf("expected open for %q, got %q", q, cls)
		}
	}
}

// TestClassifyQuestionPersonJudgment verifies that when a question directly
// targets a named person with a synthetic derogatory label, the classifier
// returns person_judgment. Approved Russian vocabulary is never embedded
// in this test; the vocabulary is parameterized.
//
// This test will not compile because ClassifyQuestionWithVocabulary does
// not exist yet.
func TestClassifyQuestionPersonJudgment(t *testing.T) {
	cls := ClassifyQuestionWithVocabulary("Is X a LABEL?", []string{"LABEL"})
	if cls != "person_judgment" {
		t.Fatalf("expected person_judgment for person-targeted label, got %q", cls)
	}
}

// TestEightBallClosedAnswerFormat verifies that a closed answer rendered
// with a synthetic neutral tail produces exactly one verdict line followed
// by the tail on its own line. The tail is caller-supplied so approved
// Russian copy never appears in this test file.
//
// This test will not compile because RenderClosedAnswer does not exist yet.
func TestEightBallClosedAnswerFormat(t *testing.T) {
	got := RenderClosedAnswer("Да.", "TAIL")
	if got != "🎱 Да.\nTAIL" {
		t.Fatalf("unexpected closed answer format: %q", got)
	}
}
