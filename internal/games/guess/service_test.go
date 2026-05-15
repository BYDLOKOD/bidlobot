package guess

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"
)

// memStore is a tiny in-memory Store for tests.
type memStore struct {
	mu     sync.Mutex
	rounds map[int64]Round     // key: absChatID
	wins   map[string]WinEntry // key: absChatID|userID
}

func newMemStore() *memStore {
	return &memStore{
		rounds: make(map[int64]Round),
		wins:   make(map[string]WinEntry),
	}
}

func winKey(chatID, userID int64) string {
	return string(rune(chatID&0xffff)) + "/" + string(rune(userID&0xffff))
}

func (m *memStore) GetRound(_ context.Context, absChatID int64) (*Round, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rounds[absChatID]
	if !ok {
		return nil, ErrNotFound
	}
	cp := r
	return &cp, nil
}

func (m *memStore) PutRound(_ context.Context, r Round) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rounds[r.AbsChatID] = r
	return nil
}

func (m *memStore) DeleteRound(_ context.Context, absChatID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rounds, absChatID)
	return nil
}

func (m *memStore) IncrementWin(_ context.Context, e WinEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := winKey(e.AbsChatID, e.UserID)
	cur, ok := m.wins[k]
	if !ok {
		cur = WinEntry{AbsChatID: e.AbsChatID, UserID: e.UserID}
	}
	cur.Wins++
	if e.Username != "" {
		cur.Username = e.Username
	}
	if e.FirstName != "" {
		cur.FirstName = e.FirstName
	}
	if !e.LastWonAt.IsZero() {
		cur.LastWonAt = e.LastWonAt
	}
	m.wins[k] = cur
	return nil
}

func (m *memStore) TopWins(_ context.Context, absChatID int64, limit int) ([]WinEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []WinEntry
	for _, e := range m.wins {
		if e.AbsChatID == absChatID {
			all = append(all, e)
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Wins != all[j].Wins {
			return all[i].Wins > all[j].Wins
		}
		return all[i].LastWonAt.Before(all[j].LastWonAt)
	})
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// fixedRand returns a constant from Intn so the secret is deterministic.
type fixedRand struct{ v int }

func (f fixedRand) Intn(int) int { return f.v }

func TestStartCreatesRound(t *testing.T) {
	store := newMemStore()
	// Intn(100) -> 41, secret = 1 + 41 = 42.
	svc := NewService(store, fixedRand{41}, nil)
	out, err := svc.Start(context.Background(), 1000, time.Now())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !out.Started || out.Recycled {
		t.Errorf("first start should be Started, not Recycled: %+v", out)
	}
	r, _ := store.GetRound(context.Background(), 1000)
	if r.Secret != 42 || !r.Active {
		t.Errorf("round mismatch: %+v", r)
	}
}

func TestStartSecondTimeReportsExisting(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, fixedRand{41}, nil)
	if _, err := svc.Start(context.Background(), 1000, time.Now()); err != nil {
		t.Fatal(err)
	}
	out, err := svc.Start(context.Background(), 1000, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if out.Started {
		t.Error("second start with active round must not start a new one")
	}
	if out.Existing == nil || out.Existing.Secret != 42 {
		t.Errorf("Existing should carry the live round, got %+v", out.Existing)
	}
}

func TestStartRecyclesStaleRound(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, fixedRand{10}, nil) // secret 11
	t0 := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	if _, err := svc.Start(context.Background(), 1000, t0); err != nil {
		t.Fatal(err)
	}
	// Two hours later the round is stale (StaleAfter == 1h).
	out, err := svc.Start(context.Background(), 1000, t0.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !out.Started || !out.Recycled {
		t.Errorf("stale round should be recycled into a fresh start: %+v", out)
	}
	r, _ := store.GetRound(context.Background(), 1000)
	if r.Secret != 11 || !r.Active {
		t.Errorf("recycled round should be fresh: %+v", r)
	}
}

func TestStartRejectsZeroChat(t *testing.T) {
	svc := NewService(newMemStore(), fixedRand{0}, nil)
	if _, err := svc.Start(context.Background(), 0, time.Now()); err == nil {
		t.Error("zero chat must be rejected")
	}
}

func TestStartRejectsNilRand(t *testing.T) {
	svc := NewService(newMemStore(), nil, nil)
	if _, err := svc.Start(context.Background(), 1000, time.Now()); err == nil {
		t.Error("nil rand must be rejected, not panic")
	}
}

func TestGuessTooLowThenTooHighThenCorrect(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, fixedRand{49}, nil) // secret 50
	if _, err := svc.Start(context.Background(), 1000, time.Now()); err != nil {
		t.Fatal(err)
	}

	low, err := svc.Guess(context.Background(), 1000, 25, 200, "alice", "Alice", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !low.TooLow || low.Correct || low.Attempts != 1 {
		t.Errorf("expected TooLow attempt 1, got %+v", low)
	}

	high, err := svc.Guess(context.Background(), 1000, 80, 200, "alice", "Alice", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !high.TooHigh || high.Attempts != 2 {
		t.Errorf("expected TooHigh attempt 2, got %+v", high)
	}

	hit, err := svc.Guess(context.Background(), 1000, 50, 300, "bob", "Bob", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !hit.Correct || hit.Secret != 50 || hit.Attempts != 3 {
		t.Errorf("expected Correct secret 50 attempt 3, got %+v", hit)
	}

	// Round must be gone after a correct guess.
	if _, err := store.GetRound(context.Background(), 1000); err != ErrNotFound {
		t.Errorf("round should be deleted after a win, got %v", err)
	}
	// Winner gets a leaderboard entry.
	top, _ := svc.Top(context.Background(), 1000, 5)
	if len(top) != 1 || top[0].UserID != 300 || top[0].Wins != 1 {
		t.Errorf("winner leaderboard mismatch: %+v", top)
	}
}

func TestGuessNoActiveRound(t *testing.T) {
	svc := NewService(newMemStore(), fixedRand{0}, nil)
	_, err := svc.Guess(context.Background(), 1000, 50, 200, "a", "A", time.Now())
	if err != ErrNotFound {
		t.Errorf("guess with no round should return ErrNotFound, got %v", err)
	}
}

func TestGuessRejectsOutOfRange(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, fixedRand{0}, nil)
	if _, err := svc.Start(context.Background(), 1000, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, v := range []int{0, -5, 101, 1000} {
		if _, err := svc.Guess(context.Background(), 1000, v, 200, "a", "A", time.Now()); err == nil {
			t.Errorf("value %d should be rejected", v)
		}
	}
	// Attempts must not have been incremented by rejected guesses.
	r, _ := store.GetRound(context.Background(), 1000)
	if r.Attempts != 0 {
		t.Errorf("rejected guesses must not bump attempts, got %d", r.Attempts)
	}
}

func TestStatusReflectsActiveRound(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, fixedRand{9}, nil) // secret 10
	if got, _ := svc.Status(context.Background(), 1000); got != nil {
		t.Errorf("no round -> nil status, got %+v", got)
	}
	if _, err := svc.Start(context.Background(), 1000, time.Now()); err != nil {
		t.Fatal(err)
	}
	st, err := svc.Status(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if st == nil || st.Secret != 10 {
		t.Errorf("status should reflect active round, got %+v", st)
	}
	// After a correct guess status is nil again.
	if _, err := svc.Guess(context.Background(), 1000, 10, 200, "a", "A", time.Now()); err != nil {
		t.Fatal(err)
	}
	if st, _ := svc.Status(context.Background(), 1000); st != nil {
		t.Errorf("status should be nil after the round ends, got %+v", st)
	}
}

func TestSeparateChatsIndependent(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, fixedRand{0}, nil) // secret 1 everywhere
	if _, err := svc.Start(context.Background(), 1000, time.Now()); err != nil {
		t.Fatal(err)
	}
	out, err := svc.Start(context.Background(), 2000, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !out.Started {
		t.Errorf("a different chat must get its own fresh round, got %+v", out)
	}
	// Guessing in chat 2000 must not touch chat 1000's round.
	if _, err := svc.Guess(context.Background(), 2000, 1, 200, "a", "A", time.Now()); err != nil {
		t.Fatal(err)
	}
	if st, _ := svc.Status(context.Background(), 1000); st == nil {
		t.Error("chat 1000 round must survive a win in chat 2000")
	}
}

func TestTopOrdersByWinsThenEarliest(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, fixedRand{0}, nil) // secret always 1
	t0 := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)

	win := func(userID int64, name string, at time.Time) {
		if _, err := svc.Start(context.Background(), 1000, at); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Guess(context.Background(), 1000, 1, userID, name, name, at); err != nil {
			t.Fatal(err)
		}
	}
	win(200, "alice", t0)
	win(300, "bob", t0.Add(time.Minute))
	win(200, "alice", t0.Add(2*time.Minute)) // alice now has 2

	top, err := svc.Top(context.Background(), 1000, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 2 || top[0].UserID != 200 || top[0].Wins != 2 || top[1].UserID != 300 {
		t.Errorf("leaderboard order wrong: %+v", top)
	}
}
