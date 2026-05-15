package bot

import (
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

// TestNewAppInitializesCooldown locks in the race fix: NewApp must
// eagerly construct the cooldown so gateMsg never lazily writes to
// a.cooldown. The previous lazy `if a.cooldown == nil` init raced (and
// could construct duplicate cooldowns) under telego's concurrent
// goroutine-per-update dispatch - exactly during the flood the gate
// exists to stop.
func TestNewAppInitializesCooldown(t *testing.T) {
	a := NewApp(nil, nil, testLogger(), nil, nil, nil, nil, nil, nil, nil)
	if a.cooldown == nil {
		t.Fatal("NewApp must eagerly initialize cooldown (race fix)")
	}
}

// TestGateMsgConcurrentSafe exercises gateMsg from many goroutines at
// once. Pre-fix this data-raced on the a.cooldown field; with eager
// init it is race-free (run under -race) and the per-user gate still
// holds: a second immediate call by the same user is dropped.
func TestGateMsgConcurrentSafe(t *testing.T) {
	a := NewApp(nil, nil, testLogger(), nil, nil, nil, nil, nil, nil, nil)

	var passed sync.Map // userID -> firstCallAllowed
	noop := func(_ *th.Context, _ telego.Message) error { return nil }
	gated := a.gateMsg("dice", time.Hour, noop)

	const n = 64
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(uid int64) {
			defer wg.Done()
			msg := telego.Message{From: &telego.User{ID: uid}}
			_ = gated(nil, msg)
			passed.Store(uid, true)
		}(int64(i + 1))
	}
	wg.Wait()

	// Each distinct user's first call was allowed; an immediate second
	// call by user 1 must be blocked by the (now race-free) gate.
	if a.cooldown.allow(1, "dice", time.Hour) {
		t.Fatal("user 1 already consumed its slot; second call must be blocked")
	}
	count := 0
	passed.Range(func(_, _ any) bool { count++; return true })
	if count != n {
		t.Fatalf("expected all %d goroutines to complete, got %d", n, count)
	}
}

func TestCooldownGate(t *testing.T) {
	c := newCooldown()
	if !c.allow(1, "dice", time.Hour) {
		t.Fatal("first call must pass")
	}
	if c.allow(1, "dice", time.Hour) {
		t.Fatal("immediate second call by same user must be blocked")
	}
	if !c.allow(2, "dice", time.Hour) {
		t.Fatal("a different user must not be blocked by user 1's cooldown")
	}
	if !c.allow(1, "quiz", time.Hour) {
		t.Fatal("a different command must have its own cooldown")
	}
	if !c.allow(1, "dice", time.Nanosecond) {
		t.Fatal("after the interval elapses the call must pass again")
	}
}

func TestStrconvI(t *testing.T) {
	for _, c := range []struct {
		in   int64
		want string
	}{{0, "0"}, {7, "7"}, {100, "100"}, {-42, "-42"}, {9223372036854775807, "9223372036854775807"}} {
		if got := strconvI(c.in); got != c.want {
			t.Errorf("strconvI(%d)=%q want %q", c.in, got, c.want)
		}
	}
}
