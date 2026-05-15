package hangman

import (
	"context"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"
)

// memStore is a tiny in-memory Store for tests.
type memStore struct {
	mu     sync.Mutex
	rounds map[int64]Round
}

func newMemStore() *memStore { return &memStore{rounds: make(map[int64]Round)} }

func (m *memStore) GetRound(_ context.Context, absChatID int64) (*Round, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rounds[absChatID]
	if !ok {
		return nil, ErrNotFound
	}
	// Deep-copy the map so callers cannot mutate stored state in place.
	cp := r
	cp.Used = make(map[string]bool, len(r.Used))
	for k, v := range r.Used {
		cp.Used[k] = v
	}
	return &cp, nil
}

func (m *memStore) PutRound(_ context.Context, r Round) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := r
	cp.Used = make(map[string]bool, len(r.Used))
	for k, v := range r.Used {
		cp.Used[k] = v
	}
	m.rounds[r.AbsChatID] = cp
	return nil
}

func (m *memStore) DeleteRound(_ context.Context, absChatID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rounds, absChatID)
	return nil
}

// startWith forces a known secret by seeding the store directly, so the
// guess-flow tests do not depend on the random word pool.
func startWith(t *testing.T, store *memStore, chatID int64, word string) *Service {
	t.Helper()
	svc := NewService(store, rand.New(rand.NewSource(1)), nil)
	store.PutRound(context.Background(), Round{
		AbsChatID: chatID,
		Word:      strings.ToUpper(word),
		Used:      make(map[string]bool),
		Active:    true,
		StartedAt: time.Now().UTC(),
	})
	return svc
}

func TestStartCreatesRound(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, rand.New(rand.NewSource(1)), nil)
	out, err := svc.Start(context.Background(), 1000, time.Now())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !out.Started || out.Recycled {
		t.Errorf("first start should be Started: %+v", out)
	}
	r, _ := store.GetRound(context.Background(), 1000)
	if r == nil || r.Word == "" || !r.Active {
		t.Errorf("round mismatch: %+v", r)
	}
	if r.Word != strings.ToUpper(r.Word) {
		t.Errorf("word must be uppercased, got %q", r.Word)
	}
}

func TestStartSecondTimeReportsExisting(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, rand.New(rand.NewSource(1)), nil)
	if _, err := svc.Start(context.Background(), 1000, time.Now()); err != nil {
		t.Fatal(err)
	}
	out, err := svc.Start(context.Background(), 1000, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if out.Started || out.Existing == nil {
		t.Errorf("active round must block a new start, got %+v", out)
	}
}

func TestStartRecyclesStaleRound(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, rand.New(rand.NewSource(1)), nil)
	t0 := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	if _, err := svc.Start(context.Background(), 1000, t0); err != nil {
		t.Fatal(err)
	}
	out, err := svc.Start(context.Background(), 1000, t0.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !out.Started || !out.Recycled {
		t.Errorf("stale round should be recycled: %+v", out)
	}
}

func TestGuessHitRevealsLetters(t *testing.T) {
	store := newMemStore()
	svc := startWith(t, store, 1000, "go")

	out, err := svc.Guess(context.Background(), 1000, "g")
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != GuessHit {
		t.Errorf("expected GuessHit, got %v", out.Result)
	}
	if out.Masked != "G_" {
		t.Errorf("expected mask G_, got %q", out.Masked)
	}
	if out.WrongLeft != MaxWrong {
		t.Errorf("a hit must not consume the wrong budget, got %d", out.WrongLeft)
	}
}

func TestGuessMissDecrementsBudget(t *testing.T) {
	store := newMemStore()
	svc := startWith(t, store, 1000, "go")

	out, err := svc.Guess(context.Background(), 1000, "z")
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != GuessMiss || out.WrongLeft != MaxWrong-1 {
		t.Errorf("miss should drop budget to %d, got %+v", MaxWrong-1, out)
	}
}

func TestGuessWinsOnFullReveal(t *testing.T) {
	store := newMemStore()
	svc := startWith(t, store, 1000, "go")
	if _, err := svc.Guess(context.Background(), 1000, "g"); err != nil {
		t.Fatal(err)
	}
	out, err := svc.Guess(context.Background(), 1000, "o")
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != GuessWon {
		t.Errorf("revealing the last letter must win, got %v", out.Result)
	}
	if out.Word != "GO" {
		t.Errorf("win outcome should expose the word, got %q", out.Word)
	}
	if _, err := store.GetRound(context.Background(), 1000); err != ErrNotFound {
		t.Errorf("round should be deleted after a win, got %v", err)
	}
}

func TestGuessLosesAfterMaxWrong(t *testing.T) {
	store := newMemStore()
	svc := startWith(t, store, 1000, "go")
	wrong := []string{"a", "b", "c", "d", "e", "f"} // none in "GO"
	var last *GuessOutcome
	for i, w := range wrong {
		o, err := svc.Guess(context.Background(), 1000, w)
		if err != nil {
			t.Fatalf("guess %q (#%d): %v", w, i, err)
		}
		last = o
	}
	if last.Result != GuessLost {
		t.Errorf("the %dth wrong guess must lose, got %v", MaxWrong, last.Result)
	}
	if last.Word != "GO" {
		t.Errorf("loss outcome should reveal the word, got %q", last.Word)
	}
	if _, err := store.GetRound(context.Background(), 1000); err != ErrNotFound {
		t.Errorf("round should be deleted after a loss, got %v", err)
	}
}

func TestGuessRejectsMultiChar(t *testing.T) {
	store := newMemStore()
	svc := startWith(t, store, 1000, "golang")
	if _, err := svc.Guess(context.Background(), 1000, "ab"); err != ErrBadLetter {
		t.Errorf("multi-char guess must be ErrBadLetter, got %v", err)
	}
	if _, err := svc.Guess(context.Background(), 1000, "5"); err != ErrBadLetter {
		t.Errorf("digit guess must be ErrBadLetter, got %v", err)
	}
	if _, err := svc.Guess(context.Background(), 1000, ""); err != ErrBadLetter {
		t.Errorf("empty guess must be ErrBadLetter, got %v", err)
	}
	// A rejected guess must not have consumed the wrong budget.
	r, _ := store.GetRound(context.Background(), 1000)
	if r.WrongCount != 0 {
		t.Errorf("bad guesses must not bump wrong count, got %d", r.WrongCount)
	}
}

func TestGuessAlreadyUsed(t *testing.T) {
	store := newMemStore()
	svc := startWith(t, store, 1000, "go")
	if _, err := svc.Guess(context.Background(), 1000, "g"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Guess(context.Background(), 1000, "G"); err != ErrAlreadyUsed {
		t.Errorf("repeating a letter (case-insensitive) must be ErrAlreadyUsed, got %v", err)
	}
}

func TestGuessCaseInsensitive(t *testing.T) {
	store := newMemStore()
	svc := startWith(t, store, 1000, "go")
	out, err := svc.Guess(context.Background(), 1000, "G") // uppercase input
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != GuessHit || out.Masked != "G_" {
		t.Errorf("uppercase input should hit a lowercase word, got %+v", out)
	}
}

func TestGuessCyrillicWord(t *testing.T) {
	store := newMemStore()
	svc := startWith(t, store, 1000, "горутина")
	out, err := svc.Guess(context.Background(), 1000, "г")
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != GuessHit {
		t.Errorf("cyrillic letter should hit cyrillic word, got %v", out.Result)
	}
	if !strings.HasPrefix(out.Masked, "Г") {
		t.Errorf("mask should reveal Г, got %q", out.Masked)
	}
	o2, err := svc.Guess(context.Background(), 1000, "Ы") // not in word
	if err != nil {
		t.Fatal(err)
	}
	if o2.Result != GuessMiss {
		t.Errorf("cyrillic miss expected, got %v", o2.Result)
	}
}

func TestGuessNoActiveRound(t *testing.T) {
	svc := NewService(newMemStore(), rand.New(rand.NewSource(1)), nil)
	if _, err := svc.Guess(context.Background(), 1000, "a"); err != ErrNotFound {
		t.Errorf("guess with no round must be ErrNotFound, got %v", err)
	}
}

func TestStatusAndMaskHelpers(t *testing.T) {
	store := newMemStore()
	svc := startWith(t, store, 1000, "go")
	if _, err := svc.Guess(context.Background(), 1000, "g"); err != nil {
		t.Fatal(err)
	}
	st, err := svc.Status(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if st == nil {
		t.Fatal("status should reflect the active round")
	}
	if MaskFor(st) != "G_" {
		t.Errorf("MaskFor mismatch: %q", MaskFor(st))
	}
	used := SortedUsed(st)
	if len(used) != 1 || used[0] != "G" {
		t.Errorf("SortedUsed mismatch: %+v", used)
	}
}

func TestWordPoolHealthy(t *testing.T) {
	if WordCount() < 120 {
		t.Errorf("word pool too small for replayability: %d", WordCount())
	}
	r := rand.New(rand.NewSource(7))
	for i := 0; i < 50; i++ {
		w := PickWord(r)
		if w == "" || w != strings.ToUpper(w) {
			t.Fatalf("PickWord returned non-uppercase or empty: %q", w)
		}
	}
}

// TestWordPoolInvariants scans the ENTIRE pool deterministically (the
// random sampling in TestWordPoolHealthy could miss a single bad entry
// among 150+). Every word must be guessable: rune length >= 4 and only
// letters IsSingleLetter accepts (Latin a-z / Cyrillic а-я + ё); a
// digit/hyphen/space could never be guessed letter-by-letter. No
// duplicates (they would skew pick probability).
func TestWordPoolInvariants(t *testing.T) {
	seen := make(map[string]bool, WordCount())
	for _, raw := range words {
		if seen[raw] {
			t.Errorf("duplicate word in pool: %q", raw)
		}
		seen[raw] = true
		rs := []rune(raw)
		if len(rs) < 4 {
			t.Errorf("word too short for hangman: %q (%d runes)", raw, len(rs))
		}
		for _, c := range rs {
			if !IsSingleLetter(string(c)) {
				t.Errorf("word %q contains a non-guessable rune %q", raw, c)
			}
		}
	}
}

func TestIsSingleLetter(t *testing.T) {
	good := []string{"a", "Z", "г", "Я", "ё", "Ё"}
	for _, s := range good {
		if !IsSingleLetter(s) {
			t.Errorf("%q should be a single letter", s)
		}
	}
	bad := []string{"", "ab", "5", " ", "go", "1a", "!"}
	for _, s := range bad {
		if IsSingleLetter(s) {
			t.Errorf("%q should not be a single letter", s)
		}
	}
}
