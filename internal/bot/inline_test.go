package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/domain/pending"
)

func newSvcForTest() *InlineService {
	return NewInlineService(newFakePending(), testLogger())
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

func TestInlineWarnsByUser(t *testing.T) {
	svc := newSvcForTest()
	sends := sendTexts(runQuery(svc, "warns @bob"))
	if len(sends) != 1 || sends[0] != "/warns @bob" {
		t.Fatalf("expected /warns @bob, got %v", sends)
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
	for _, q := range []string{"", "stats", "stats top", "warns @bob", "help"} {
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
	for _, q := range []string{"", "stats", "stats top", "warns @bob", "help"} {
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

// Destructive previews

func TestInlineWarnPreviewCreatesPending(t *testing.T) {
	store := newFakePending()
	svc := NewInlineService(store, testLogger())

	results := svc.BuildResults(context.Background(), telego.InlineQuery{
		Query: "warn @bob spam links", From: telego.User{ID: 100},
	})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	article, _ := results[0].(*telego.InlineQueryResultArticle)
	if article.ReplyMarkup == nil || len(article.ReplyMarkup.InlineKeyboard) == 0 {
		t.Fatal("warn preview must carry confirm keyboard")
	}
	// pending action should exist
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.data) != 1 {
		t.Fatalf("expected 1 pending action, got %d", len(store.data))
	}
	for _, a := range store.data {
		if a.Kind != pending.KindWarn || a.ActorUserID != 100 || a.Reason != "spam links" {
			t.Fatalf("pending action mismatch: %+v", a)
		}
		if a.TargetDisplay != "@bob" {
			t.Fatalf("expected TargetDisplay @bob, got %q", a.TargetDisplay)
		}
		if a.ExpiresAt.Before(time.Now()) {
			t.Fatal("ExpiresAt must be in the future")
		}
	}
}

func TestInlineWarnHintForBareUser(t *testing.T) {
	svc := newSvcForTest()
	results := runQuery(svc, "warn")
	if len(results) != 1 {
		t.Fatalf("expected hint result, got %d", len(results))
	}
	article, _ := results[0].(*telego.InlineQueryResultArticle)
	if article.ReplyMarkup != nil {
		t.Fatal("hint result must not have a confirm keyboard")
	}
}

func TestInlineMutePreviewParsesDuration(t *testing.T) {
	store := newFakePending()
	svc := NewInlineService(store, testLogger())

	results := svc.BuildResults(context.Background(), telego.InlineQuery{
		Query: "mute @bob 30m", From: telego.User{ID: 100},
	})
	if len(results) != 1 {
		t.Fatalf("expected 1 result")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, a := range store.data {
		if a.Duration != 30*time.Minute {
			t.Fatalf("expected 30m duration, got %v", a.Duration)
		}
	}
}

func TestInlineMuteRejectsBadDuration(t *testing.T) {
	store := newFakePending()
	svc := NewInlineService(store, testLogger())
	results := svc.BuildResults(context.Background(), telego.InlineQuery{
		Query: "mute @bob xyz", From: telego.User{ID: 100},
	})
	if len(results) != 1 {
		t.Fatal("expected single hint result")
	}
	if len(store.data) != 0 {
		t.Fatal("no pending action must be created on bad input")
	}
}

func TestInlineCleanupParsesPeriod(t *testing.T) {
	store := newFakePending()
	svc := NewInlineService(store, testLogger())

	cases := []struct {
		query string
		want  time.Duration
	}{
		{"cleanup 6mo", 6 * 30 * 24 * time.Hour},
		{"cleanup 30d", 30 * 24 * time.Hour},
		{"cleanup 1y", 365 * 24 * time.Hour},
		{"cleanup 2w", 14 * 24 * time.Hour},
	}
	for _, c := range cases {
		store.mu.Lock()
		store.data = make(map[string]*pending.Action)
		store.mu.Unlock()

		results := svc.BuildResults(context.Background(), telego.InlineQuery{
			Query: c.query, From: telego.User{ID: 100},
		})
		if len(results) != 1 {
			t.Fatalf("%q: expected 1 result, got %d", c.query, len(results))
		}
		store.mu.Lock()
		var threshold time.Duration
		for _, a := range store.data {
			threshold = a.Threshold
		}
		store.mu.Unlock()
		if threshold != c.want {
			t.Errorf("%q: threshold %v, want %v", c.query, threshold, c.want)
		}
	}
}

func TestInlineCleanupRejectsEmpty(t *testing.T) {
	store := newFakePending()
	svc := NewInlineService(store, testLogger())
	results := svc.BuildResults(context.Background(), telego.InlineQuery{
		Query: "cleanup", From: telego.User{ID: 100},
	})
	if len(results) != 1 {
		t.Fatal("expected hint result")
	}
	if len(store.data) != 0 {
		t.Fatal("no pending must be created without period")
	}
}

func TestInlineCallbackKeyboardFitsLimit(t *testing.T) {
	id := "1234567890abcdef" // 16 chars - the longest valid pending id
	kb := confirmKeyboard(id)
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if len(btn.CallbackData) > 64 {
				t.Fatalf("callback_data exceeds 64 bytes: %q", btn.CallbackData)
			}
		}
	}
}

func TestInlineParseInlineDuration(t *testing.T) {
	cases := []struct {
		in    string
		want  time.Duration
		isErr bool
	}{
		{"30m", 30 * time.Minute, false},
		{"1h", time.Hour, false},
		{"2h30m", 2*time.Hour + 30*time.Minute, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"2w", 14 * 24 * time.Hour, false},
		{"6mo", 6 * 30 * 24 * time.Hour, false},
		{"1y", 365 * 24 * time.Hour, false},
		{"", 0, true},
		{"xyz", 0, true},
		{"0d", 0, true},
		{"abcd", 0, true},
	}
	for _, c := range cases {
		got, err := parseInlineDuration(c.in)
		if c.isErr && err == nil {
			t.Errorf("%q: expected error, got %v", c.in, got)
			continue
		}
		if !c.isErr && err != nil {
			t.Errorf("%q: unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%q: got %v, want %v", c.in, got, c.want)
		}
	}
}
