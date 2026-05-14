package profile

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestFSMCreateAndGet(t *testing.T) {
	fsm := NewFSMStore()

	fsm.Set(111, &Session{
		ChatID: 100,
		Step:   StepStack,
		Mode:   ModeRegister,
	})

	s, ok := fsm.Get(111)
	if !ok {
		t.Fatal("session not found")
	}
	if s.Step != StepStack {
		t.Fatalf("expected StepStack, got %d", s.Step)
	}
	if s.ChatID != 100 {
		t.Fatal("wrong chatID")
	}
}

func TestFSMGetReturnsCopy(t *testing.T) {
	fsm := NewFSMStore()
	fsm.Set(111, &Session{ChatID: 100, Step: StepStack})

	s, _ := fsm.Get(111)
	s.Step = StepConfirm

	s2, _ := fsm.Get(111)
	if s2.Step != StepStack {
		t.Fatal("Get should return a copy, not a reference")
	}
}

func TestFSMDelete(t *testing.T) {
	fsm := NewFSMStore()
	fsm.Set(111, &Session{ChatID: 100})

	fsm.Delete(111)

	_, ok := fsm.Get(111)
	if ok {
		t.Fatal("session should be deleted")
	}
}

func TestFSMHas(t *testing.T) {
	fsm := NewFSMStore()

	if fsm.Has(111) {
		t.Fatal("should not have session")
	}

	fsm.Set(111, &Session{ChatID: 100})
	if !fsm.Has(111) {
		t.Fatal("should have session")
	}
}

func TestFSMTimeout(t *testing.T) {
	fsm := NewFSMStore()
	fsm.Set(111, &Session{ChatID: 100})

	fsm.mu.Lock()
	fsm.sessions[111].LastTouch = time.Now().Add(-2 * time.Hour)
	fsm.mu.Unlock()

	_, ok := fsm.Get(111)
	if ok {
		t.Fatal("expired session should not be returned")
	}

	if fsm.Has(111) {
		t.Fatal("Has should return false for expired session")
	}
}

func TestFSMOneSessionPerUser(t *testing.T) {
	fsm := NewFSMStore()
	fsm.Set(111, &Session{ChatID: 100, Step: StepStack})
	fsm.Set(111, &Session{ChatID: 200, Step: StepExperience})

	s, ok := fsm.Get(111)
	if !ok {
		t.Fatal("session not found")
	}
	if s.ChatID != 200 || s.Step != StepExperience {
		t.Fatal("second Set should overwrite first")
	}
}

func TestFSMSweeper(t *testing.T) {
	fsm := NewFSMStore()
	fsm.Set(111, &Session{ChatID: 100})
	fsm.Set(222, &Session{ChatID: 100})

	fsm.mu.Lock()
	fsm.sessions[111].LastTouch = time.Now().Add(-2 * time.Hour)
	fsm.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	go fsm.RunSweeper(ctx, 10*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	cancel()

	if fsm.Has(111) {
		t.Fatal("expired session should be swept")
	}
	if !fsm.Has(222) {
		t.Fatal("active session should survive sweep")
	}
}

func TestFSMConcurrentAccess(t *testing.T) {
	fsm := NewFSMStore()
	var wg sync.WaitGroup

	for i := int64(0); i < 50; i++ {
		wg.Add(1)
		go func(uid int64) {
			defer wg.Done()
			fsm.Set(uid, &Session{ChatID: 100, Step: StepStack})
			fsm.Get(uid)
			fsm.Has(uid)
			fsm.Delete(uid)
		}(i)
	}
	wg.Wait()
}

func TestFSMStepProgression(t *testing.T) {
	steps := []Step{StepStack, StepExperience, StepBio, StepConfirm}
	for i, step := range steps {
		if int(step) != i {
			t.Fatalf("step %d has value %d, expected %d", i, step, i)
		}
	}
}
