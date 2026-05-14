package bot

import (
	"strings"
	"testing"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/games/dice"
)

func TestGamesRouterDiceDefault(t *testing.T) {
	r := NewGamesInlineRouter()
	results, ok := r.Route("dice", nil, telego.User{ID: 100})
	if !ok {
		t.Fatal("dice should be handled")
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if got := sendTexts(results); got[0] != "/dice" {
		t.Errorf("expected /dice, got %q", got[0])
	}
}

func TestGamesRouterDiceWithEmoji(t *testing.T) {
	r := NewGamesInlineRouter()
	results, ok := r.Route("dice", []string{"\U0001F3AF"}, telego.User{ID: 100})
	if !ok {
		t.Fatal("dice should be handled")
	}
	if got := sendTexts(results); got[0] != "/dice \U0001F3AF" {
		t.Errorf("expected /dice + dart, got %q", got[0])
	}
}

func TestGamesRouterDiceUnknownEmojiFallsToDefault(t *testing.T) {
	r := NewGamesInlineRouter()
	results, ok := r.Route("dice", []string{"abc"}, telego.User{ID: 100})
	if !ok {
		t.Fatal("dice should be handled even with bad emoji")
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 hint result, got %d", len(results))
	}
	got := sendTexts(results)[0]
	if got != "/dice" {
		t.Errorf("hint should fall back to /dice, got %q", got)
	}
}

func TestGamesRouterBattleHintWhenMissingArgs(t *testing.T) {
	r := NewGamesInlineRouter()
	for _, args := range [][]string{nil, {"only"}} {
		results, ok := r.Route("battle", args, telego.User{ID: 100})
		if !ok {
			t.Fatalf("battle %v should be handled", args)
		}
		if len(results) != 1 {
			t.Fatalf("battle %v: expected 1 hint, got %d", args, len(results))
		}
		body := sendTexts(results)[0]
		if body != "/help" {
			t.Errorf("battle %v: expected /help hint body, got %q", args, body)
		}
	}
}

func TestGamesRouterBattleWithTwoArgs(t *testing.T) {
	r := NewGamesInlineRouter()
	results, ok := r.Route("battle", []string{"go", "rust"}, telego.User{ID: 100})
	if !ok {
		t.Fatal("battle should be handled")
	}
	if got := sendTexts(results)[0]; got != "/battle go rust" {
		t.Errorf("expected /battle go rust, got %q", got)
	}
}

func TestGamesRouterQuizDefault(t *testing.T) {
	r := NewGamesInlineRouter()
	results, ok := r.Route("quiz", nil, telego.User{ID: 100})
	if !ok {
		t.Fatal("quiz should be handled")
	}
	if got := sendTexts(results)[0]; got != "/quiz" {
		t.Errorf("expected /quiz, got %q", got)
	}
}

func TestGamesRouterQuizTop(t *testing.T) {
	r := NewGamesInlineRouter()
	results, ok := r.Route("quiz", []string{"top"}, telego.User{ID: 100})
	if !ok {
		t.Fatal("quiz top should be handled")
	}
	if got := sendTexts(results)[0]; got != "/quiz top" {
		t.Errorf("expected /quiz top, got %q", got)
	}
}

func TestGamesRouterUnknownCommandFallsThrough(t *testing.T) {
	r := NewGamesInlineRouter()
	if _, ok := r.Route("warn", []string{"@bob"}, telego.User{ID: 100}); ok {
		t.Error("router must not claim non-game commands")
	}
}

func TestInlineServiceConsultsGameRouter(t *testing.T) {
	store := newFakePending()
	svc := NewInlineService(store, testLogger())
	svc.SetGameRouter(NewGamesInlineRouter())

	results := runQuery(svc, "dice")
	if len(results) == 0 {
		t.Fatal("dice query should yield game-router results")
	}
	if got := sendTexts(results)[0]; got != "/dice" {
		t.Errorf("expected /dice, got %q", got)
	}
}

func TestGamesInlineDiceAllEmojisRecognised(t *testing.T) {
	r := NewGamesInlineRouter()
	for _, e := range dice.AllowedEmojis {
		results, ok := r.Route("dice", []string{e}, telego.User{ID: 100})
		if !ok {
			t.Fatalf("dice %s should be handled", e)
		}
		got := sendTexts(results)[0]
		if !strings.HasPrefix(got, "/dice ") {
			t.Errorf("emoji %s: expected /dice prefix, got %q", e, got)
		}
	}
}
