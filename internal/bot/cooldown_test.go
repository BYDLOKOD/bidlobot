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
	if allowed, _ := a.cooldown.gate(1, "dice", time.Hour); allowed {
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
	if a, n := c.gate(1, "dice", time.Hour); !a || n {
		t.Fatalf("first call must pass without a notice, got allowed=%v notify=%v", a, n)
	}
	// First over-frequency call: blocked AND emits exactly one notice.
	if a, n := c.gate(1, "dice", time.Hour); a || !n {
		t.Fatalf("second call must be blocked with a notice, got allowed=%v notify=%v", a, n)
	}
	// Further rapid repeats in the same window: blocked but SILENT
	// (notice bounded to one per window - no notice spam from a flood).
	if a, n := c.gate(1, "dice", time.Hour); a || n {
		t.Fatalf("third rapid call must be blocked and silent, got allowed=%v notify=%v", a, n)
	}
	if _, n := c.gate(1, "dice", time.Hour); n {
		t.Fatal("the notice must not repeat within the same window")
	}
	if a, _ := c.gate(2, "dice", time.Hour); !a {
		t.Fatal("a different user must not be blocked by user 1's cooldown")
	}
	if a, _ := c.gate(1, "quiz", time.Hour); !a {
		t.Fatal("a different command must have its own cooldown")
	}
	// Once the interval elapses the call passes again and the notice
	// state resets (a fresh allow clears it, so the next burst is
	// acknowledged once more).
	if a, n := c.gate(1, "dice", time.Nanosecond); !a || n {
		t.Fatalf("after the interval the call must pass again, got allowed=%v notify=%v", a, n)
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
