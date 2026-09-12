package retry_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mymmrac/telego/telegoapi"

	"github.com/veschin/bidlobot/internal/shared/retry"
)

func apiErr(code int, retryAfter int) error {
	e := &telegoapi.Error{
		Description: "test",
		ErrorCode:   code,
	}
	if retryAfter > 0 {
		e.Parameters = &telegoapi.ResponseParameters{RetryAfter: retryAfter}
	}
	// Wrap like telego does so errors.As still works.
	return fmt.Errorf("api: %w", e)
}

// noJitter keeps tests deterministic.
func noJitter(d time.Duration) time.Duration { return d }

// instantSleep records sleep durations and returns immediately so tests
// stay fast.
type instantSleep struct {
	durations []time.Duration
}

func (s *instantSleep) Fn(ctx context.Context, d time.Duration) error {
	s.durations = append(s.durations, d)
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func TestSuccessFirstAttempt(t *testing.T) {
	calls := atomic.Int32{}
	err := retry.Do(context.Background(), retry.Policy{Sleep: (&instantSleep{}).Fn, Jitter: noJitter},
		func(ctx context.Context) error {
			calls.Add(1)
			return nil
		})
	if err != nil {
		t.Fatalf("got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", calls.Load())
	}
}

func Test429ThenSuccess(t *testing.T) {
	calls := atomic.Int32{}
	sleep := &instantSleep{}
	err := retry.Do(context.Background(), retry.Policy{Sleep: sleep.Fn, Jitter: noJitter},
		func(ctx context.Context) error {
			n := calls.Add(1)
			if n == 1 {
				return apiErr(429, 3)
			}
			return nil
		})
	if err != nil {
		t.Fatalf("got %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 2 calls, got %d", calls.Load())
	}
	if len(sleep.durations) != 1 || sleep.durations[0] != 3*time.Second {
		t.Fatalf("expected one 3s sleep, got %v", sleep.durations)
	}
}

func Test429TwiceFails(t *testing.T) {
	calls := atomic.Int32{}
	sleep := &instantSleep{}
	err := retry.Do(context.Background(), retry.Policy{Sleep: sleep.Fn, Jitter: noJitter},
		func(ctx context.Context) error {
			calls.Add(1)
			return apiErr(429, 1)
		})
	if err == nil {
		t.Fatal("expected error")
	}
	var apiE *telegoapi.Error
	if !errors.As(err, &apiE) || apiE.ErrorCode != 429 {
		t.Fatalf("expected 429 in chain, got %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 2 calls (initial + 1 retry), got %d", calls.Load())
	}
}

func Test429MissingRetryAfterDefaultsToOneSecond(t *testing.T) {
	calls := atomic.Int32{}
	sleep := &instantSleep{}
	_ = retry.Do(context.Background(), retry.Policy{Sleep: sleep.Fn, Jitter: noJitter},
		func(ctx context.Context) error {
			n := calls.Add(1)
			if n == 1 {
				return apiErr(429, 0) // no Parameters
			}
			return nil
		})
	if len(sleep.durations) != 1 || sleep.durations[0] != 1*time.Second {
		t.Fatalf("expected one 1s default sleep, got %v", sleep.durations)
	}
}

func Test500RetriesFourTimesThenFails(t *testing.T) {
	calls := atomic.Int32{}
	sleep := &instantSleep{}
	err := retry.Do(context.Background(), retry.Policy{Sleep: sleep.Fn, Jitter: noJitter},
		func(ctx context.Context) error {
			calls.Add(1)
			return apiErr(503, 0)
		})
	if err == nil {
		t.Fatal("expected error after exhaustion")
	}
	if calls.Load() != int32(retry.MaxServerErrorAttempts) {
		t.Fatalf("expected %d calls, got %d", retry.MaxServerErrorAttempts, calls.Load())
	}
	expected := []time.Duration{
		retry.ServerBackoff(1),
		retry.ServerBackoff(2),
		retry.ServerBackoff(3),
	}
	if len(sleep.durations) != len(expected) {
		t.Fatalf("expected %d sleeps, got %d (%v)", len(expected), len(sleep.durations), sleep.durations)
	}
	for i, want := range expected {
		if sleep.durations[i] != want {
			t.Fatalf("sleep %d: expected %s, got %s", i, want, sleep.durations[i])
		}
	}
}

func Test500ThenSuccessOnThirdAttempt(t *testing.T) {
	calls := atomic.Int32{}
	sleep := &instantSleep{}
	err := retry.Do(context.Background(), retry.Policy{Sleep: sleep.Fn, Jitter: noJitter},
		func(ctx context.Context) error {
			n := calls.Add(1)
			if n < 3 {
				return apiErr(500, 0)
			}
			return nil
		})
	if err != nil {
		t.Fatalf("got %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("expected 3 calls, got %d", calls.Load())
	}
}

func Test400DoesNotRetry(t *testing.T) {
	calls := atomic.Int32{}
	err := retry.Do(context.Background(), retry.Policy{Sleep: (&instantSleep{}).Fn, Jitter: noJitter},
		func(ctx context.Context) error {
			calls.Add(1)
			return apiErr(400, 0)
		})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 call (no retry), got %d", calls.Load())
	}
}

func Test403DoesNotRetry(t *testing.T) {
	calls := atomic.Int32{}
	_ = retry.Do(context.Background(), retry.Policy{Sleep: (&instantSleep{}).Fn, Jitter: noJitter},
		func(ctx context.Context) error {
			calls.Add(1)
			return apiErr(403, 0)
		})
	if calls.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", calls.Load())
	}
}

// TestTransportErrorRetriesThenFails pins the transport ladder: a call
// that never reaches Telegram (DNS, connect, TLS, read/write timeout) is
// attempted MaxTransportAttempts times, then the error surfaces.
func TestTransportErrorRetriesThenFails(t *testing.T) {
	calls := atomic.Int32{}
	sleep := &instantSleep{}
	want := errors.New("fasthttp do request: lookup api.telegram.org: i/o timeout")
	err := retry.Do(context.Background(), retry.Policy{Sleep: sleep.Fn, Jitter: noJitter},
		func(ctx context.Context) error {
			calls.Add(1)
			return want
		})
	if !errors.Is(err, want) {
		t.Fatalf("expected wrapped want, got %v", err)
	}
	if calls.Load() != int32(retry.MaxTransportAttempts) {
		t.Fatalf("expected %d calls, got %d", retry.MaxTransportAttempts, calls.Load())
	}
	expected := []time.Duration{retry.TransportBackoff(1), retry.TransportBackoff(2)}
	if len(sleep.durations) != len(expected) {
		t.Fatalf("expected %d sleeps, got %v", len(expected), sleep.durations)
	}
	for i, w := range expected {
		if sleep.durations[i] != w {
			t.Fatalf("sleep %d: want %s, got %s", i, w, sleep.durations[i])
		}
	}
}

// TestTransportThenSuccess: the resolver blip clears on the second
// attempt, which is the case the ladder exists for.
func TestTransportThenSuccess(t *testing.T) {
	calls := atomic.Int32{}
	err := retry.Do(context.Background(), retry.Policy{Sleep: (&instantSleep{}).Fn, Jitter: noJitter},
		func(ctx context.Context) error {
			if calls.Add(1) == 1 {
				return errors.New("dial tcp: connection reset by peer")
			}
			return nil
		})
	if err != nil {
		t.Fatalf("got %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 2 calls, got %d", calls.Load())
	}
}

// TestCanceledCallIsNotRetried: a cancelled caller means shutdown or an
// expired handler, not a transient fault - retrying only delays the exit.
func TestCanceledCallIsNotRetried(t *testing.T) {
	calls := atomic.Int32{}
	err := retry.Do(context.Background(), retry.Policy{Sleep: (&instantSleep{}).Fn, Jitter: noJitter},
		func(ctx context.Context) error {
			calls.Add(1)
			return fmt.Errorf("send: %w", context.Canceled)
		})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", calls.Load())
	}
}

// TestAttemptTimeoutBoundsEachAttempt: with AttemptTimeout set, a call
// that hangs is cut off per attempt and retried, so Do returns instead of
// blocking forever on a stalled transport.
func TestAttemptTimeoutBoundsEachAttempt(t *testing.T) {
	calls := atomic.Int32{}
	start := time.Now()
	err := retry.Do(context.Background(),
		retry.Policy{Sleep: (&instantSleep{}).Fn, Jitter: noJitter, AttemptTimeout: 30 * time.Millisecond},
		func(ctx context.Context) error {
			calls.Add(1)
			<-ctx.Done()
			return ctx.Err()
		})
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	if calls.Load() != int32(retry.MaxTransportAttempts) {
		t.Fatalf("expected %d attempts, got %d", retry.MaxTransportAttempts, calls.Load())
	}
	if elapsed > 2*time.Second {
		t.Fatalf("hung for %s", elapsed)
	}
}

// TestAttemptTimeoutLeavesCallerContextAlone: the per-attempt deadline
// must not be charged to the caller context, which stays usable.
func TestAttemptTimeoutLeavesCallerContextAlone(t *testing.T) {
	ctx := context.Background()
	calls := atomic.Int32{}
	err := retry.Do(ctx, retry.Policy{Sleep: (&instantSleep{}).Fn, Jitter: noJitter, AttemptTimeout: time.Second},
		func(c context.Context) error {
			if calls.Add(1) == 1 {
				return errors.New("temporary failure in name resolution")
			}
			return nil
		})
	if err != nil {
		t.Fatalf("got %v", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("caller context was cancelled: %v", ctx.Err())
	}
}

func TestTransportBackoffLadder(t *testing.T) {
	want := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second}
	for i, w := range want {
		if got := retry.TransportBackoff(i + 1); got != w {
			t.Fatalf("attempt %d: want %s, got %s", i+1, w, got)
		}
	}
}

func TestContextCancelDuringWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := atomic.Int32{}
	// Use real sleep so the cancel can race against time.After.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	err := retry.Do(ctx, retry.Policy{Jitter: noJitter},
		func(c context.Context) error {
			calls.Add(1)
			return apiErr(503, 0)
		})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	// Allow the in-flight call to count, but no full ladder.
	if calls.Load() >= int32(retry.MaxServerErrorAttempts) {
		t.Fatalf("expected early abort, got %d calls", calls.Load())
	}
}

func TestContextAlreadyDoneShortCircuits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := atomic.Int32{}
	err := retry.Do(ctx, retry.Policy{Jitter: noJitter},
		func(c context.Context) error {
			calls.Add(1)
			return nil
		})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled, got %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("expected 0 calls, got %d", calls.Load())
	}
}

func TestServerBackoffLadder(t *testing.T) {
	want := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second}
	for i, w := range want {
		got := retry.ServerBackoff(i + 1)
		if got != w {
			t.Fatalf("attempt %d: want %s, got %s", i+1, w, got)
		}
	}
}

func TestJitterStaysWithinBand(t *testing.T) {
	const trials = 200
	for i := 0; i < trials; i++ {
		got := retry.DefaultJitter(10 * time.Second)
		if got < 9*time.Second || got > 11*time.Second {
			t.Fatalf("jitter out of [9s, 11s]: %s", got)
		}
	}
}
