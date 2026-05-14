package ratelimit_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/veschin/bidlobot/internal/shared/ratelimit"
)

func newTestLimiter(t *testing.T, rate time.Duration) *ratelimit.Limiter {
	t.Helper()
	l := ratelimit.New(ratelimit.Config{
		Rate:           rate,
		QueueCapacity:  ratelimit.DefaultQueueCapacity,
		IdleTimeout:    50 * time.Millisecond,
		ReaperInterval: 10 * time.Millisecond,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(func() { l.Close() })
	return l
}

// TestSingleSlotImmediate verifies the first call returns immediately
// because the bucket starts with a free slot (nextAllowedAt is zero).
func TestSingleSlotImmediate(t *testing.T) {
	l := newTestLimiter(t, 100*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := l.Wait(ctx, 1); err != nil {
		t.Fatalf("first Wait: %v", err)
	}
	if dur := time.Since(start); dur > 30*time.Millisecond {
		t.Fatalf("first call should be fast, took %s", dur)
	}
}

// TestRateReplenishment verifies that the second call to the same chat
// is delayed by Rate.
func TestRateReplenishment(t *testing.T) {
	rate := 30 * time.Millisecond
	l := newTestLimiter(t, rate)

	ctx := context.Background()
	if err := l.Wait(ctx, 7); err != nil {
		t.Fatalf("first Wait: %v", err)
	}

	start := time.Now()
	if err := l.Wait(ctx, 7); err != nil {
		t.Fatalf("second Wait: %v", err)
	}
	dur := time.Since(start)
	if dur < rate-2*time.Millisecond {
		t.Fatalf("second call should wait at least %s, took %s", rate, dur)
	}
	if dur > rate+50*time.Millisecond {
		t.Fatalf("second call should not wait more than %s, took %s", rate+50*time.Millisecond, dur)
	}
}

// TestPerChatIsolation: a slow chat does not block another chat's traffic.
func TestPerChatIsolation(t *testing.T) {
	l := newTestLimiter(t, 200*time.Millisecond)

	// Saturate chat 1.
	if err := l.Wait(context.Background(), 1); err != nil {
		t.Fatalf("chat 1 first: %v", err)
	}

	// chat 2 must be served immediately - independent bucket.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := l.Wait(ctx, 2); err != nil {
		t.Fatalf("chat 2 first: %v", err)
	}
	if dur := time.Since(start); dur > 30*time.Millisecond {
		t.Fatalf("chat 2 should not wait for chat 1, took %s", dur)
	}
}

// TestQueueOverflowDropsOldest: filling beyond queue capacity drops the
// oldest waiters with ErrQueueFull while newer waiters remain queued.
//
// We use Rate = 1h so the worker latches the next slot far in the future
// after consuming the priming Wait. Then we enqueue cap+evict additional
// requests synchronously: each enqueueLocked call must evict the oldest
// when the queue is at capacity.
func TestQueueOverflowDropsOldest(t *testing.T) {
	queueCap := 3
	evict := 2
	// Total producers = 1 (held by worker, sleeping on slot) + queueCap
	// (waiting in queue) + evict (forced out). The first goroutine is
	// popped by the worker before subsequent enqueues happen.
	totalProducers := 1 + queueCap + evict
	l := ratelimit.New(ratelimit.Config{
		Rate:           1 * time.Hour,
		QueueCapacity:  queueCap,
		IdleTimeout:    1 * time.Hour,
		ReaperInterval: 1 * time.Hour,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(func() { l.Close() })

	// Prime: take the free slot. Worker is now sleeping ~1h.
	if err := l.Wait(context.Background(), 99); err != nil {
		t.Fatalf("priming: %v", err)
	}
	// Let the worker pick up the priming reflexively before staggering.
	time.Sleep(20 * time.Millisecond)

	type result struct {
		slot int
		err  error
	}
	resCh := make(chan result, totalProducers)
	for i := 0; i < totalProducers; i++ {
		go func(slot int) {
			err := l.Wait(context.Background(), 99)
			resCh <- result{slot, err}
		}(i)
		// Stagger producers so the queue ordering is deterministic. Worker
		// pops the first producer before others enqueue, so the queue
		// holds slots [1..queueCap+evict].
		time.Sleep(15 * time.Millisecond)
	}

	got := make(map[int]error, evict)
	deadline := time.After(3 * time.Second)
	for len(got) < evict {
		select {
		case r := <-resCh:
			got[r.slot] = r.err
		case <-deadline:
			t.Fatalf("expected %d evictions, got %d (results=%v)", evict, len(got), got)
		}
	}

	// Slots 1 and 2 are the oldest queued (slot 0 is held by the worker).
	for slot := 1; slot <= evict; slot++ {
		err, ok := got[slot]
		if !ok {
			t.Fatalf("slot %d: not evicted (got=%v)", slot, got)
		}
		if !errors.Is(err, ratelimit.ErrQueueFull) {
			t.Fatalf("slot %d: expected ErrQueueFull, got %v", slot, err)
		}
	}
}

// TestFIFOWithinChat: 3 sequential waiters get served in arrival order.
func TestFIFOWithinChat(t *testing.T) {
	l := newTestLimiter(t, 30*time.Millisecond)

	// Saturate the bucket so subsequent Wait calls queue.
	if err := l.Wait(context.Background(), 5); err != nil {
		t.Fatal(err)
	}

	const n = 4
	order := make(chan int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if err := l.Wait(context.Background(), 5); err == nil {
				order <- idx
			}
		}(i)
		// Stagger producers to fix the queue order.
		time.Sleep(2 * time.Millisecond)
	}
	wg.Wait()
	close(order)

	got := make([]int, 0, n)
	for v := range order {
		got = append(got, v)
	}
	if len(got) != n {
		t.Fatalf("expected %d completions, got %d", n, len(got))
	}
	for i := 0; i < n; i++ {
		if got[i] != i {
			t.Fatalf("FIFO violated: got %v", got)
		}
	}
}

// TestContextCancelReturnsImmediately: a long Wait is canceled by ctx,
// the producer sees ctx.Err.
func TestContextCancelReturnsImmediately(t *testing.T) {
	l := newTestLimiter(t, 1*time.Second)
	if err := l.Wait(context.Background(), 1); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Wait(ctx, 1) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected ctx.Canceled, got %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Wait did not return after cancel")
	}
}

// TestThirtyConcurrentSerialized verifies that 30 simultaneous producers
// for the same chat are serialized at the configured rate.
func TestThirtyConcurrentSerialized(t *testing.T) {
	const n = 30
	rate := 5 * time.Millisecond
	l := newTestLimiter(t, rate)

	var ok atomic.Int32
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := l.Wait(ctx, 7); err == nil {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	if got := ok.Load(); got != n {
		t.Fatalf("expected %d successes, got %d", n, got)
	}
	min := rate * (n - 1)
	if elapsed < min-2*time.Millisecond {
		t.Fatalf("expected at least %s for %d serialized waits, got %s", min, n, elapsed)
	}
}

// TestCloseRejectsPending: queued waiters all get ErrLimiterClosed.
func TestCloseRejectsPending(t *testing.T) {
	l := ratelimit.New(ratelimit.Config{
		Rate:           1 * time.Hour,
		QueueCapacity:  10,
		IdleTimeout:    1 * time.Hour,
		ReaperInterval: 1 * time.Hour,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err := l.Wait(context.Background(), 1); err != nil {
		t.Fatal(err)
	}

	const n = 5
	results := make([]chan error, n)
	for i := range results {
		results[i] = make(chan error, 1)
		go func(slot int) { results[slot] <- l.Wait(context.Background(), 1) }(i)
	}
	time.Sleep(50 * time.Millisecond)

	l.Close()

	for i, ch := range results {
		select {
		case err := <-ch:
			if !errors.Is(err, ratelimit.ErrLimiterClosed) {
				t.Fatalf("slot %d: expected ErrLimiterClosed, got %v", i, err)
			}
		case <-time.After(200 * time.Millisecond):
			t.Fatalf("slot %d: no result after Close", i)
		}
	}

	// Subsequent Wait must fail immediately.
	if err := l.Wait(context.Background(), 2); !errors.Is(err, ratelimit.ErrLimiterClosed) {
		t.Fatalf("Wait after Close: expected ErrLimiterClosed, got %v", err)
	}
}

// TestIdleReaper: an unused chat bucket is removed after IdleTimeout.
func TestIdleReaper(t *testing.T) {
	l := ratelimit.New(ratelimit.Config{
		Rate:           5 * time.Millisecond,
		QueueCapacity:  5,
		IdleTimeout:    20 * time.Millisecond,
		ReaperInterval: 10 * time.Millisecond,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(func() { l.Close() })

	if err := l.Wait(context.Background(), 1234); err != nil {
		t.Fatal(err)
	}
	if l.ChatCount() != 1 {
		t.Fatalf("expected 1 bucket, got %d", l.ChatCount())
	}

	// Wait long enough for idle timeout + reaper sweep.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if l.ChatCount() == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected idle bucket to be reaped, ChatCount=%d", l.ChatCount())
}

// TestCloseIsIdempotent: calling Close twice is safe.
func TestCloseIsIdempotent(t *testing.T) {
	l := newTestLimiter(t, 10*time.Millisecond)
	l.Close()
	l.Close()
}
