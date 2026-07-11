package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/mymmrac/telego"
)

func newSvcForTest() *InlineService {
	return NewInlineService(testLogger())
}

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

func runQuery(svc *InlineService, q string) []telego.InlineQueryResult {
	return svc.BuildResults(context.Background(), telego.InlineQuery{
		Query: q,
		From:  telego.User{ID: 100},
	})
}

func TestInlineEmptyQueryReturnsCatalog(t *testing.T) {
	svc := newSvcForTest()
	results := runQuery(svc, "")
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
	svc := newSvcForTest()
	a := runQuery(svc, "")
	b := runQuery(svc, "   ")
	if len(a) != len(b) {
		t.Fatalf("whitespace should equal empty: %d vs %d", len(a), len(b))
	}
}

func TestInlineStatsBareReturnsAllStatsVariants(t *testing.T) {
	svc := newSvcForTest()
	results := runQuery(svc, "stats")
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
	svc := newSvcForTest()
	sends := sendTexts(runQuery(svc, "stats top"))
	if len(sends) != 1 || sends[0] != "/stats top" {
		t.Fatalf("expected exactly /stats top, got %v", sends)
	}
}

func TestInlineStatsTodayExact(t *testing.T) {
	svc := newSvcForTest()
	sends := sendTexts(runQuery(svc, "stats today"))
	if len(sends) != 1 || sends[0] != "/stats today" {
		t.Fatalf("expected exactly /stats today, got %v", sends)
	}
}

func TestInlineStatsByUsername(t *testing.T) {
	svc := newSvcForTest()
	sends := sendTexts(runQuery(svc, "stats @alice"))
	if len(sends) != 1 || sends[0] != "/stats @alice" {
		t.Fatalf("expected /stats @alice, got %v", sends)
	}
}

func TestInlineHelpReturnsHelp(t *testing.T) {
	svc := newSvcForTest()
	sends := sendTexts(runQuery(svc, "help"))
	if len(sends) != 1 || sends[0] != "/help" {
		t.Fatalf("expected /help, got %v", sends)
	}
}

func TestInlineUnknownQueryFiltersCatalog(t *testing.T) {
	svc := newSvcForTest()
	results := runQuery(svc, "stat")
	if len(results) == 0 {
		t.Fatal("expected at least the stats entries to match prefix 'stat'")
	}
	for _, r := range results {
		article, _ := r.(*telego.InlineQueryResultArticle)
		hay := strings.ToLower(article.Title + article.Description)
		if !strings.Contains(hay, "stat") {
			t.Errorf("filter should keep only matches, got %q", article.Title)
		}
	}
}

func TestInlineUnknownQueryFallsBackToFullCatalog(t *testing.T) {
	svc := newSvcForTest()
	results := runQuery(svc, "xxxxxxxxx_no_match")
	if len(results) == 0 {
		t.Fatal("no-match query should still return catalog as fallback")
	}
}

func TestInlineResultsHaveStableIDs(t *testing.T) {
	svc := newSvcForTest()
	for _, q := range []string{"", "stats", "stats top", "help"} {
		for _, r := range runQuery(svc, q) {
			article, _ := r.(*telego.InlineQueryResultArticle)
			if article.ID == "" {
				t.Fatalf("query %q: empty ID", q)
			}
			if len(article.ID) > 64 {
				t.Fatalf("query %q: ID exceeds 64 bytes: %q", q, article.ID)
			}
		}
	}
}

func TestInlineResultsAreArticleType(t *testing.T) {
	svc := newSvcForTest()
	for _, q := range []string{"", "stats", "stats top", "help"} {
		for _, r := range runQuery(svc, q) {
			article, ok := r.(*telego.InlineQueryResultArticle)
			if !ok {
				t.Fatalf("query %q: expected Article type, got %T", q, r)
			}
			if article.Type != telego.ResultTypeArticle {
				t.Fatalf("query %q: expected Type=article", q)
			}
		}
	}
}
