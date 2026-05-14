// Package retry implements Telegram-aware retry with backoff for outbound
// Bot API calls. Two policies fold into one Do call:
//
//   - 429 Too Many Requests: sleep for the server-supplied retry_after
//     plus 10% jitter and try once more. A second 429 fails fast - if
//     Telegram throttles us harder than we provisioned, we want loud
//     visibility, not silent stacking.
//
//   - 5xx Server Error: exponential backoff (1s, 2s, 4s, 8s) with 10%
//     jitter, max four attempts total. 5xx is almost always transient
//     (proxy hiccup, brief Telegram outage); the bounded ladder hides
//     the user-visible blip without becoming a retry storm.
//
// Other 4xx (400, 403, ...) are not retried because they signal a real
// problem in the request (bad chat ID, blocked by user, etc.) that
// retrying cannot fix. They surface to the caller immediately.
//
// Context cancellation aborts the wait: if the bot is shutting down or
// the per-handler context expires, no further attempts run.
package retry

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/mymmrac/telego/telegoapi"
)

// MaxServerErrorAttempts caps total attempts on 5xx so a long Telegram
// outage cannot block a handler indefinitely. Four attempts at 1+2+4+8s
// = 15s of waiting plus jitter.
const MaxServerErrorAttempts = 4

// ServerBackoff returns the delay before attempt n (1-indexed) in the
// 5xx ladder. Pure for testing.
func ServerBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := time.Second << (attempt - 1)
	if base > 8*time.Second {
		base = 8 * time.Second
	}
	return base
}

// Jitter adds +/- 10% noise to d so concurrent retries spread their wakes.
type Jitter func(d time.Duration) time.Duration

// DefaultJitter applies +/- 10% randomness using crypto-quality enough
// math/rand/v2; for retry pacing this is sufficient.
func DefaultJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	// rand.Float64 is in [0, 1). Map to [-0.1, +0.1).
	delta := (rand.Float64()*0.2 - 0.1) // nolint:gosec
	return d + time.Duration(float64(d)*delta)
}

// Sleep is the contract for time.After-style waits; injectable so tests
// can run without real wall-clock waits.
type Sleep func(ctx context.Context, d time.Duration) error

// DefaultSleep blocks until d elapses or ctx is canceled. Returns ctx.Err
// in the cancellation case so the caller can short-circuit.
func DefaultSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Policy bundles the tunables for Do; pass [Policy{}] for defaults.
type Policy struct {
	Sleep                  Sleep
	Jitter                 Jitter
	MaxServerErrorAttempts int
}

func (p *Policy) ensureDefaults() {
	if p.Sleep == nil {
		p.Sleep = DefaultSleep
	}
	if p.Jitter == nil {
		p.Jitter = DefaultJitter
	}
	if p.MaxServerErrorAttempts <= 0 {
		p.MaxServerErrorAttempts = MaxServerErrorAttempts
	}
}

// Do executes fn under the policy. fn returns the error from one Bot API
// call (typically a wrapped *telegoapi.Error). Do unwraps it, decides
// whether to retry, sleeps if needed, and loops until either success,
// non-retryable error, or attempt budget exhaustion. The final returned
// error is whatever the last attempt produced.
func Do(ctx context.Context, p Policy, fn func(ctx context.Context) error) error {
	p.ensureDefaults()

	var lastErr error
	retried429 := false
	serverAttempt := 1

	for {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return fmt.Errorf("retry aborted: %w (last attempt: %w)", err, lastErr)
			}
			return err
		}

		err := fn(ctx)
		if err == nil {
			return nil
		}
		lastErr = err

		decision := classify(err)
		switch decision.kind {
		case kindNonRetryable:
			return err

		case kindTooManyRequests:
			if retried429 {
				return err
			}
			retried429 = true
			delay := time.Duration(decision.retryAfter) * time.Second
			if delay <= 0 {
				delay = 1 * time.Second
			}
			delay = p.Jitter(delay)
			if waitErr := p.Sleep(ctx, delay); waitErr != nil {
				return waitErr
			}

		case kindServerError:
			if serverAttempt >= p.MaxServerErrorAttempts {
				return err
			}
			delay := p.Jitter(ServerBackoff(serverAttempt))
			serverAttempt++
			if waitErr := p.Sleep(ctx, delay); waitErr != nil {
				return waitErr
			}

		default:
			return err
		}
	}
}

// classification of an error returned by the API call.
type kind int

const (
	kindNonRetryable kind = iota
	kindTooManyRequests
	kindServerError
)

type decision struct {
	kind       kind
	retryAfter int
}

func classify(err error) decision {
	if err == nil {
		return decision{kind: kindNonRetryable}
	}
	var apiErr *telegoapi.Error
	if !errors.As(err, &apiErr) {
		return decision{kind: kindNonRetryable}
	}
	switch {
	case apiErr.ErrorCode == 429:
		ra := 0
		if apiErr.Parameters != nil {
			ra = apiErr.Parameters.RetryAfter
		}
		return decision{kind: kindTooManyRequests, retryAfter: ra}
	case apiErr.ErrorCode >= 500 && apiErr.ErrorCode <= 599:
		return decision{kind: kindServerError}
	default:
		return decision{kind: kindNonRetryable}
	}
}
