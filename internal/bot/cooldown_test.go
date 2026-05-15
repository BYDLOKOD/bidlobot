package bot

import (
	"testing"
	"time"
)

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
