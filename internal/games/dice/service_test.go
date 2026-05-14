package dice

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// memStore is a tiny in-memory Store for tests.
type memStore struct {
	mu   sync.Mutex
	data map[string]Record // key: absChatID|emoji
}

func newMemStore() *memStore { return &memStore{data: make(map[string]Record)} }

func key(chatID int64, emoji string) string {
	return fmt.Sprintf("%d:%s", chatID, emoji)
}

func (m *memStore) Get(_ context.Context, absChatID int64, emoji string) (*Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.data[key(absChatID, emoji)]
	if !ok {
		return nil, ErrNotFound
	}
	cp := r
	return &cp, nil
}

func (m *memStore) Put(_ context.Context, r Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key(r.AbsChatID, r.Emoji)] = r
	return nil
}

func TestSubmitRollFirstRecord(t *testing.T) {
	svc := NewService(newMemStore(), nil)
	ts := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)

	out, err := svc.SubmitRoll(context.Background(), 1000, DefaultEmoji, 6, 200, "alice", "Alice", ts)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !out.NewRecord {
		t.Error("first record must be marked as new")
	}
	if out.Tied {
		t.Error("first record cannot be a tie")
	}
	if out.Previous != nil {
		t.Error("first record should have nil Previous")
	}
	if out.Recorded.Value != 6 || out.Recorded.UserID != 200 {
		t.Errorf("recorded mismatch: %+v", out.Recorded)
	}
}

func TestSubmitRollImprovement(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, nil)
	ts := time.Now().UTC()

	if _, err := svc.SubmitRoll(context.Background(), 1000, DefaultEmoji, 4, 200, "alice", "Alice", ts); err != nil {
		t.Fatalf("first: %v", err)
	}
	out, err := svc.SubmitRoll(context.Background(), 1000, DefaultEmoji, 6, 300, "bob", "Bob", ts.Add(time.Second))
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !out.NewRecord {
		t.Error("higher value must be marked as new record")
	}
	if out.Previous == nil || out.Previous.Value != 4 || out.Previous.UserID != 200 {
		t.Errorf("Previous should reflect old record, got %+v", out.Previous)
	}
	if out.Recorded.UserID != 300 || out.Recorded.Value != 6 {
		t.Errorf("Recorded should reflect new record, got %+v", out.Recorded)
	}
}

func TestSubmitRollTieKeepsOlder(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, nil)
	t1 := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)

	if _, err := svc.SubmitRoll(context.Background(), 1000, DefaultEmoji, 6, 200, "alice", "Alice", t1); err != nil {
		t.Fatal(err)
	}
	out, err := svc.SubmitRoll(context.Background(), 1000, DefaultEmoji, 6, 300, "bob", "Bob", t2)
	if err != nil {
		t.Fatal(err)
	}
	if out.NewRecord {
		t.Error("tie must not be a new record")
	}
	if !out.Tied {
		t.Error("equal value should be flagged as tie")
	}
	if out.Recorded.UserID != 200 {
		t.Errorf("tie should keep older record holder; got user %d", out.Recorded.UserID)
	}

	// store should still hold the original
	got, _ := store.Get(context.Background(), 1000, DefaultEmoji)
	if got.UserID != 200 {
		t.Errorf("store should retain original holder; got %+v", got)
	}
}

func TestSubmitRollLowerValue(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, nil)
	ts := time.Now().UTC()

	_, _ = svc.SubmitRoll(context.Background(), 1000, DefaultEmoji, 6, 200, "alice", "Alice", ts)
	out, err := svc.SubmitRoll(context.Background(), 1000, DefaultEmoji, 3, 300, "bob", "Bob", ts.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if out.NewRecord || out.Tied {
		t.Errorf("lower roll must not affect record; got %+v", out)
	}
	if out.Recorded.UserID != 200 {
		t.Errorf("lower roll should not displace existing record; got %+v", out.Recorded)
	}
}

func TestSubmitRollSeparateEmojisIndependent(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, nil)
	ts := time.Now().UTC()

	if _, err := svc.SubmitRoll(context.Background(), 1000, DefaultEmoji, 6, 200, "alice", "Alice", ts); err != nil {
		t.Fatal(err)
	}
	// 🎯 should be independent of 🎲
	out, err := svc.SubmitRoll(context.Background(), 1000, "\U0001F3AF", 5, 300, "bob", "Bob", ts)
	if err != nil {
		t.Fatal(err)
	}
	if !out.NewRecord || out.Previous != nil {
		t.Errorf("different emoji must start fresh leaderboard; got %+v", out)
	}
}

func TestSubmitRollSeparateChatsIndependent(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, nil)
	ts := time.Now().UTC()

	if _, err := svc.SubmitRoll(context.Background(), 1000, DefaultEmoji, 6, 200, "alice", "Alice", ts); err != nil {
		t.Fatal(err)
	}
	out, err := svc.SubmitRoll(context.Background(), 2000, DefaultEmoji, 4, 300, "bob", "Bob", ts)
	if err != nil {
		t.Fatal(err)
	}
	if !out.NewRecord {
		t.Errorf("different chat must start fresh leaderboard; got %+v", out)
	}
}

func TestSubmitRollRejectsBadEmoji(t *testing.T) {
	svc := NewService(newMemStore(), nil)
	if _, err := svc.SubmitRoll(context.Background(), 1000, "X", 1, 200, "", "", time.Now()); err == nil {
		t.Error("expected error for bad emoji")
	}
}

func TestSubmitRollRejectsBadValue(t *testing.T) {
	svc := NewService(newMemStore(), nil)
	cases := []struct {
		name  string
		value int
	}{
		{"zero", 0},
		{"negative", -1},
		{"too_high_dice", 7},
	}
	for _, c := range cases {
		if _, err := svc.SubmitRoll(context.Background(), 1000, DefaultEmoji, c.value, 200, "", "", time.Now()); err == nil {
			t.Errorf("%s: expected error for value %d", c.name, c.value)
		}
	}
}

func TestSubmitRollRejectsZeroIDs(t *testing.T) {
	svc := NewService(newMemStore(), nil)
	if _, err := svc.SubmitRoll(context.Background(), 0, DefaultEmoji, 1, 200, "", "", time.Now()); err == nil {
		t.Error("expected error for zero chatID")
	}
	if _, err := svc.SubmitRoll(context.Background(), 1000, DefaultEmoji, 1, 0, "", "", time.Now()); err == nil {
		t.Error("expected error for zero userID")
	}
}

func TestSubmitRollSlotMachineUpTo64(t *testing.T) {
	svc := NewService(newMemStore(), nil)
	if _, err := svc.SubmitRoll(context.Background(), 1000, "\U0001F3B0", 64, 200, "", "", time.Now()); err != nil {
		t.Errorf("slot machine 64 should be valid: %v", err)
	}
	if _, err := svc.SubmitRoll(context.Background(), 1000, "\U0001F3B0", 65, 200, "", "", time.Now()); err == nil {
		t.Error("slot machine value 65 should be rejected")
	}
}

func TestTopReturnsRecord(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, nil)
	ts := time.Now().UTC()

	if _, err := svc.SubmitRoll(context.Background(), 1000, DefaultEmoji, 6, 200, "alice", "Alice", ts); err != nil {
		t.Fatal(err)
	}
	r, err := svc.Top(context.Background(), 1000, DefaultEmoji)
	if err != nil {
		t.Fatal(err)
	}
	if r == nil || r.UserID != 200 || r.Value != 6 {
		t.Errorf("Top mismatch: %+v", r)
	}
}

func TestTopReturnsNilWhenAbsent(t *testing.T) {
	svc := NewService(newMemStore(), nil)
	r, err := svc.Top(context.Background(), 1000, DefaultEmoji)
	if err != nil {
		t.Fatal(err)
	}
	if r != nil {
		t.Errorf("expected nil for empty leaderboard, got %+v", r)
	}
}

func TestIsAllowedEmoji(t *testing.T) {
	for _, e := range AllowedEmojis {
		if !IsAllowedEmoji(e) {
			t.Errorf("%s should be allowed", e)
		}
	}
	if IsAllowedEmoji("X") {
		t.Error("X should not be allowed")
	}
	if IsAllowedEmoji("") {
		t.Error("empty should not be allowed")
	}
}
