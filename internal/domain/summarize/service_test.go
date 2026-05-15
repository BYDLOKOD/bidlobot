package summarize

import (
	"testing"
	"time"
)

func TestGlobalAllow_CapEnforcedThenWindowFrees(t *testing.T) {
	s := NewService(NewBuffer(BufferConfig{}), nil, Config{
		GlobalMaxCalls: 3,
		GlobalWindow:   time.Hour,
	}, nil)

	for i := 0; i < 3; i++ {
		if !s.GlobalAllow() {
			t.Fatalf("call %d within cap must be allowed", i+1)
		}
	}
	if s.GlobalAllow() {
		t.Fatalf("4th call must be denied (cap is 3)")
	}

	// Simulate the window having elapsed by ageing the recorded
	// timestamps past the window; the next call must be allowed again.
	s.gmu.Lock()
	for i := range s.gcalls {
		s.gcalls[i] = s.gcalls[i].Add(-2 * time.Hour)
	}
	s.gmu.Unlock()
	if !s.GlobalAllow() {
		t.Fatalf("after the window elapsed the cap must reset")
	}
}

func TestNewService_GlobalDefaults(t *testing.T) {
	s := NewService(NewBuffer(BufferConfig{}), nil, Config{}, nil)
	if s.globalMax != defaultGlobalMaxCalls || s.globalWindow != defaultGlobalWindow {
		t.Fatalf("defaults not applied: max=%d window=%s", s.globalMax, s.globalWindow)
	}
	if s.inputBudget != defaultInputBudget {
		t.Fatalf("input budget default = %d, want %d", s.inputBudget, defaultInputBudget)
	}
}
