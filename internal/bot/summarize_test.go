package bot

import "testing"

func TestParseSummarizeArgs(t *testing.T) {
	tests := []struct {
		input string
		wantN int
		wantQ string
	}{
		{"/summarize", summarizeDefaultN, ""},
		{"/summarize 500", 500, ""},
		{"/summarize 200 что решили по деплою?", 200, "что решили по деплою?"},
		{"/итог что решили?", summarizeDefaultN, "что решили?"},
		{"/summarize@BotName 300", 300, ""},
		{"/summarize@BotName 100 кто обсуждал архитектуру?", 100, "кто обсуждал архитектуру?"},
		{"/summarize что решили? и какие планы?", summarizeDefaultN, "что решили? и какие планы?"},
		{"/summarize 5000", summarizeMaxN, ""},
		{"/summarize @mention 200", 200, ""},
	}

	for _, tc := range tests {
		got := parseSummarizeArgs(tc.input)
		if got.n != tc.wantN {
			t.Errorf("parseSummarizeArgs(%q).n = %d, want %d", tc.input, got.n, tc.wantN)
		}
		if got.questions != tc.wantQ {
			t.Errorf("parseSummarizeArgs(%q).questions = %q, want %q", tc.input, got.questions, tc.wantQ)
		}
	}
}
