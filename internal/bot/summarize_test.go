package bot

import (
	"strings"
	"testing"
	"time"

	"github.com/veschin/bidlobot/internal/domain/summarize"
)

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

func TestComposeSummaryMessageAppendsGenerationCost(t *testing.T) {
	meta := summarize.Meta{
		Included:          300,
		From:              time.Date(2026, 7, 15, 17, 24, 0, 0, time.UTC),
		To:                time.Date(2026, 7, 16, 6, 2, 0, 0, time.UTC),
		GenerationCostUSD: 0.001784,
	}
	got := composeSummaryMessage("Основной разговор был о защите данных.", meta, "veschin", nil)
	if !strings.Contains(got, "итог 300 сообщений") {
		t.Fatalf("footer missing included count: %q", got)
	}
	if !strings.HasSuffix(got, "расчетная стоимость: $0.0018") {
		t.Fatalf("footer must end with rounded generation cost: %q", got)
	}
}
