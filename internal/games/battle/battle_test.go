package battle

import (
	"sync"
	"testing"
	"time"
)

func TestNewBattleRejectsBadLabels(t *testing.T) {
	cases := []struct {
		name        string
		left, right string
	}{
		{"empty_left", "", "go"},
		{"empty_right", "go", ""},
		{"whitespace_left", "   ", "go"},
		{"both_empty", "", ""},
	}
	for _, c := range cases {
		if _, err := NewBattle("id", 100, c.left, c.right, time.Now(), 0); err == nil {
			t.Errorf("%s: expected error", c.name)
		}
	}
}

func TestNewBattleRejectsTooLongLabels(t *testing.T) {
	long := make([]byte, MaxLabelLen+1)
	for i := range long {
		long[i] = 'x'
	}
	if _, err := NewBattle("id", 100, string(long), "go", time.Now(), 0); err == nil {
		t.Error("expected error for over-long left label")
	}
	if _, err := NewBattle("id", 100, "go", string(long), time.Now(), 0); err == nil {
		t.Error("expected error for over-long right label")
	}
}

func TestNewBattleAppliesDefaultDurationOnZero(t *testing.T) {
	start := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	b, err := NewBattle("id", 100, "go", "rust", start, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !b.EndsAt.Equal(start.Add(DefaultDuration)) {
		t.Errorf("expected end at start+%s, got %v", DefaultDuration, b.EndsAt)
	}
}

func TestRecordVoteFirstReturnsTrue(t *testing.T) {
	b, _ := NewBattle("id", 100, "go", "rust", time.Now(), 0)
	if !b.RecordVote(200, SideLeft) {
		t.Error("first vote on left should return true")
	}
}

func TestRecordVoteDuplicateReturnsFalse(t *testing.T) {
	b, _ := NewBattle("id", 100, "go", "rust", time.Now(), 0)
	b.RecordVote(200, SideLeft)
	if b.RecordVote(200, SideLeft) {
		t.Error("duplicate vote on same side should return false")
	}
}

func TestRecordVoteOppositeSidesIndependent(t *testing.T) {
	// A user can vote on both sides; the rules say "ever reacted",
	// counted per (user, side). The tally then shows them on both.
	b, _ := NewBattle("id", 100, "go", "rust", time.Now(), 0)
	if !b.RecordVote(200, SideLeft) {
		t.Error("left vote rejected")
	}
	if !b.RecordVote(200, SideRight) {
		t.Error("right vote should be accepted independently")
	}
	r := b.Tally(time.Now())
	if r.LeftVotes != 1 || r.RightVotes != 1 || !r.Tied {
		t.Errorf("expected 1-1 tie, got %+v", r)
	}
}

func TestRecordVoteRejectsZeroUserID(t *testing.T) {
	b, _ := NewBattle("id", 100, "go", "rust", time.Now(), 0)
	if b.RecordVote(0, SideLeft) {
		t.Error("zero user ID must be rejected")
	}
}

func TestRecordVoteRejectsUnknownSide(t *testing.T) {
	b, _ := NewBattle("id", 100, "go", "rust", time.Now(), 0)
	if b.RecordVote(200, Side(99)) {
		t.Error("unknown side must be rejected")
	}
}

func TestTallyEmptyIsTie(t *testing.T) {
	b, _ := NewBattle("id", 100, "go", "rust", time.Now(), 0)
	r := b.Tally(time.Now())
	if !r.Tied || !r.NoVotes {
		t.Errorf("empty tally must be tied + no-votes, got %+v", r)
	}
}

func TestTallyLeftWins(t *testing.T) {
	b, _ := NewBattle("id", 100, "go", "rust", time.Now(), 0)
	for _, uid := range []int64{1, 2, 3} {
		b.RecordVote(uid, SideLeft)
	}
	b.RecordVote(4, SideRight)
	r := b.Tally(time.Now())
	if r.Tied || r.NoVotes {
		t.Errorf("expected decisive result, got %+v", r)
	}
	if r.WinnerSide != SideLeft {
		t.Errorf("expected left winner, got %v", r.WinnerSide)
	}
	if r.LeftVotes != 3 || r.RightVotes != 1 {
		t.Errorf("counts wrong: %+v", r)
	}
}

func TestTallyRightWins(t *testing.T) {
	b, _ := NewBattle("id", 100, "go", "rust", time.Now(), 0)
	b.RecordVote(1, SideLeft)
	for _, uid := range []int64{2, 3, 4} {
		b.RecordVote(uid, SideRight)
	}
	r := b.Tally(time.Now())
	if r.WinnerSide != SideRight {
		t.Errorf("expected right winner, got %v", r.WinnerSide)
	}
}

func TestRegistryAddAndGet(t *testing.T) {
	r := NewRegistry()
	b, _ := NewBattle("abc", 100, "go", "rust", time.Now(), 0)
	r.Add(b)
	if r.Get("abc") != b {
		t.Error("Get should return the added battle")
	}
	if r.Active() != 1 {
		t.Errorf("expected 1 active, got %d", r.Active())
	}
}

func TestRegistrySetMessageIDsLookup(t *testing.T) {
	r := NewRegistry()
	b, _ := NewBattle("abc", 100, "go", "rust", time.Now(), 0)
	r.Add(b)
	r.SetMessageIDs("abc", 10, 20)

	got, side, ok := r.LookupByMessageID(10)
	if !ok || side != SideLeft || got != b {
		t.Errorf("left lookup mismatch: %v %v %v", got, side, ok)
	}
	got, side, ok = r.LookupByMessageID(20)
	if !ok || side != SideRight || got != b {
		t.Errorf("right lookup mismatch: %v %v %v", got, side, ok)
	}
	if _, _, ok := r.LookupByMessageID(99); ok {
		t.Error("unknown message ID must miss")
	}
}

func TestRegistrySetMessageIDsRebindRemovesOld(t *testing.T) {
	r := NewRegistry()
	b, _ := NewBattle("abc", 100, "go", "rust", time.Now(), 0)
	r.Add(b)
	r.SetMessageIDs("abc", 10, 20)
	r.SetMessageIDs("abc", 11, 21)

	if _, _, ok := r.LookupByMessageID(10); ok {
		t.Error("old left ID should no longer resolve")
	}
	if _, _, ok := r.LookupByMessageID(11); !ok {
		t.Error("new left ID should resolve")
	}
}

func TestRegistryRemove(t *testing.T) {
	r := NewRegistry()
	b, _ := NewBattle("abc", 100, "go", "rust", time.Now(), 0)
	r.Add(b)
	r.SetMessageIDs("abc", 10, 20)
	r.Remove("abc")
	if r.Get("abc") != nil {
		t.Error("Get must return nil after Remove")
	}
	if _, _, ok := r.LookupByMessageID(10); ok {
		t.Error("LookupByMessageID must miss after Remove")
	}
	if r.Active() != 0 {
		t.Errorf("expected 0 active, got %d", r.Active())
	}
}

func TestRecordVoteIsConcurrencySafe(t *testing.T) {
	// Sanity check: ensure parallel votes do not duplicate counts.
	b, _ := NewBattle("abc", 100, "go", "rust", time.Now(), 0)
	const N = 1000
	var wg sync.WaitGroup
	for i := 1; i <= N; i++ {
		wg.Add(1)
		go func(uid int64) {
			defer wg.Done()
			b.RecordVote(uid, SideLeft)
		}(int64(i))
	}
	wg.Wait()
	r := b.Tally(time.Now())
	if r.LeftVotes != N {
		t.Errorf("expected %d left votes, got %d", N, r.LeftVotes)
	}
}
