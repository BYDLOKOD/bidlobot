package duel

import "testing"

func TestDecideChallengerWins(t *testing.T) {
	r, err := Decide(6, 3)
	if err != nil {
		t.Fatal(err)
	}
	if r.Winner != SideChallenger {
		t.Errorf("6 vs 3: challenger should win, got %v", r.Winner)
	}
	if r.ChallengerVal != 6 || r.OpponentVal != 3 {
		t.Errorf("values not echoed: %+v", r)
	}
}

func TestDecideOpponentWins(t *testing.T) {
	r, err := Decide(2, 5)
	if err != nil {
		t.Fatal(err)
	}
	if r.Winner != SideOpponent {
		t.Errorf("2 vs 5: opponent should win, got %v", r.Winner)
	}
}

func TestDecideTie(t *testing.T) {
	r, err := Decide(4, 4)
	if err != nil {
		t.Fatal(err)
	}
	if r.Winner != SideTie {
		t.Errorf("4 vs 4: tie expected, got %v", r.Winner)
	}
}

func TestDecideRejectsOutOfRange(t *testing.T) {
	cases := [][2]int{{0, 3}, {7, 3}, {3, 0}, {3, 7}, {-1, 2}}
	for _, c := range cases {
		if _, err := Decide(c[0], c[1]); err == nil {
			t.Errorf("Decide%v should reject out-of-range values", c)
		}
	}
}

func TestParseOpponentBasic(t *testing.T) {
	op, err := ParseOpponent("/duel @bob", "alice", "bidlobot")
	if err != nil {
		t.Fatal(err)
	}
	if op.Username != "bob" || op.Display != "@bob" {
		t.Errorf("unexpected opponent: %+v", op)
	}
}

func TestParseOpponentWithoutAtSign(t *testing.T) {
	op, err := ParseOpponent("/duel bob", "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	if op.Username != "bob" || op.Display != "@bob" {
		t.Errorf("bare handle should still parse: %+v", op)
	}
}

func TestParseOpponentNoTarget(t *testing.T) {
	if _, err := ParseOpponent("/duel", "alice", "bot"); err != ErrNoTarget {
		t.Errorf("missing target should be ErrNoTarget, got %v", err)
	}
	if _, err := ParseOpponent("/duel    ", "alice", "bot"); err != ErrNoTarget {
		t.Errorf("whitespace-only arg should be ErrNoTarget, got %v", err)
	}
}

func TestParseOpponentRejectsGarbage(t *testing.T) {
	// Only the first token after /duel is the opponent handle; a garbage
	// first token (punctuation, non-ASCII, over-long) is rejected.
	for _, bad := range []string{"/duel !!!", "/duel ра反бот", "/duel " + longName()} {
		if _, err := ParseOpponent(bad, "alice", "bot"); err != ErrNoTarget {
			t.Errorf("%q should be ErrNoTarget, got %v", bad, err)
		}
	}
}

func TestParseOpponentUsesFirstTokenOnly(t *testing.T) {
	// Trailing words (a jokey "reason") are ignored - the opponent is
	// the first token only.
	op, err := ParseOpponent("/duel @bob на слабо", "alice", "bot")
	if err != nil {
		t.Fatalf("trailing words must be ignored, got %v", err)
	}
	if op.Username != "bob" {
		t.Errorf("opponent should be bob, got %q", op.Username)
	}
}

func TestParseOpponentSelf(t *testing.T) {
	if _, err := ParseOpponent("/duel @alice", "alice", "bot"); err != ErrSelfTarget {
		t.Errorf("self-duel should be ErrSelfTarget, got %v", err)
	}
	// Case-insensitive self detection.
	if _, err := ParseOpponent("/duel @Alice", "alice", "bot"); err != ErrSelfTarget {
		t.Errorf("case-insensitive self-duel should be ErrSelfTarget, got %v", err)
	}
}

func TestParseOpponentBot(t *testing.T) {
	if _, err := ParseOpponent("/duel @BidloBot", "alice", "bidlobot"); err != ErrBotTarget {
		t.Errorf("dueling the bot should be ErrBotTarget, got %v", err)
	}
	// Unknown bot username -> bot check skipped, parses fine.
	if _, err := ParseOpponent("/duel @bidlobot", "alice", ""); err != nil {
		t.Errorf("empty botUsername should skip the bot check, got %v", err)
	}
}

func longName() string {
	b := make([]byte, 33)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}
