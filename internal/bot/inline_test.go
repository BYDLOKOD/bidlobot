package bot

import (
	"strings"
	"testing"

	"github.com/mymmrac/telego"
)

func sendTexts(results []telego.InlineQueryResult) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		article, ok := r.(*telego.InlineQueryResultArticle)
		if !ok {
			continue
		}
		content, ok := article.InputMessageContent.(*telego.InputTextMessageContent)
		if !ok {
			continue
		}
		out = append(out, content.MessageText)
	}
	return out
}

func TestInlineEmptyQueryReturnsCatalog(t *testing.T) {
	results := buildInlineResults("")
	if len(results) == 0 {
		t.Fatal("empty query should return catalog")
	}
	sends := sendTexts(results)
	wantContains := []string{"/stats", "/help"}
	for _, w := range wantContains {
		found := false
		for _, s := range sends {
			if s == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("catalog missing %q (got %v)", w, sends)
		}
	}
}

func TestInlineWhitespaceQueryReturnsCatalog(t *testing.T) {
	a := buildInlineResults("")
	b := buildInlineResults("   ")
	if len(a) != len(b) {
		t.Fatalf("whitespace should equal empty: %d vs %d", len(a), len(b))
	}
}

func TestInlineStatsBareReturnsAllStatsVariants(t *testing.T) {
	results := buildInlineResults("stats")
	sends := sendTexts(results)
	want := []string{"/stats", "/stats top", "/stats today"}
	for _, w := range want {
		found := false
		for _, s := range sends {
			if s == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected %q in stats results, got %v", w, sends)
		}
	}
}

func TestInlineStatsTopExact(t *testing.T) {
	results := buildInlineResults("stats top")
	sends := sendTexts(results)
	if len(sends) != 1 || sends[0] != "/stats top" {
		t.Fatalf("expected exactly /stats top, got %v", sends)
	}
}

func TestInlineStatsTodayExact(t *testing.T) {
	results := buildInlineResults("stats today")
	sends := sendTexts(results)
	if len(sends) != 1 || sends[0] != "/stats today" {
		t.Fatalf("expected exactly /stats today, got %v", sends)
	}
}

func TestInlineStatsByUsername(t *testing.T) {
	results := buildInlineResults("stats @alice")
	sends := sendTexts(results)
	if len(sends) != 1 {
		t.Fatalf("expected single result, got %d", len(sends))
	}
	if sends[0] != "/stats @alice" {
		t.Fatalf("expected /stats @alice, got %q", sends[0])
	}
}

func TestInlineWarnsByUser(t *testing.T) {
	results := buildInlineResults("warns @bob")
	sends := sendTexts(results)
	if len(sends) != 1 || sends[0] != "/warns @bob" {
		t.Fatalf("expected /warns @bob, got %v", sends)
	}
}

func TestInlineWarnsBareShowsHint(t *testing.T) {
	results := buildInlineResults("warns")
	sends := sendTexts(results)
	if len(sends) != 1 || sends[0] != "/warns" {
		t.Fatalf("expected hint /warns, got %v", sends)
	}
	article, _ := results[0].(*telego.InlineQueryResultArticle)
	if !strings.Contains(strings.ToLower(article.Description), "укажите") {
		t.Errorf("description should hint at usage, got %q", article.Description)
	}
}

func TestInlineHelpReturnsHelp(t *testing.T) {
	results := buildInlineResults("help")
	sends := sendTexts(results)
	if len(sends) != 1 || sends[0] != "/help" {
		t.Fatalf("expected /help, got %v", sends)
	}
}

func TestInlineUnknownQueryFiltersCatalog(t *testing.T) {
	results := buildInlineResults("st")
	sends := sendTexts(results)
	if len(sends) == 0 {
		t.Fatal("expected at least the stats entries to match prefix 'st'")
	}
	for _, s := range sends {
		if !strings.Contains(strings.ToLower(s), "st") {
			t.Errorf("catalog filter should keep only matches, got %q", s)
		}
	}
}

func TestInlineUnknownQueryFallsBackToFullCatalogWhenNoMatch(t *testing.T) {
	results := buildInlineResults("xxxxxxxxx_no_match")
	if len(results) == 0 {
		t.Fatal("no-match query should still return catalog as fallback")
	}
}

func TestInlineResultsHaveStableIDs(t *testing.T) {
	results := buildInlineResults("stats @alice")
	for _, r := range results {
		article, _ := r.(*telego.InlineQueryResultArticle)
		if article.ID == "" {
			t.Fatal("inline result must have non-empty ID")
		}
		if len(article.ID) > 64 {
			t.Fatalf("inline result ID exceeds 64 bytes: %q", article.ID)
		}
	}
}

func TestInlineResultsAreArticleType(t *testing.T) {
	for _, q := range []string{"", "stats", "stats top", "warns @bob", "help"} {
		results := buildInlineResults(q)
		for _, r := range results {
			article, ok := r.(*telego.InlineQueryResultArticle)
			if !ok {
				t.Fatalf("query %q: expected Article type, got %T", q, r)
			}
			if article.Type != telego.ResultTypeArticle {
				t.Fatalf("query %q: expected Type=article, got %q", q, article.Type)
			}
		}
	}
}
