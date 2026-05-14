// Package ratelimit implements per-chat outgoing rate limiting for the
// Telegram Bot API. Telegram documents 20 requests/minute per group;
// 15/min leaves headroom for bursts and admin-cache refreshes.
//
// Each chat owns an independent token bucket replenished at one token per
// 4 seconds (15/min) and a bounded FIFO queue (capacity 50). Excess work
// drops the oldest waiter rather than blocking the producer indefinitely:
// for a moderation bot, stale messages are worse than rejected ones.
//
// A worker goroutine is spawned lazily on the first request to a chat and
// reaped after [idleTimeout] of inactivity, so a bot watching thousands
// of chats does not pay for goroutines it never uses.
package ratelimit

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// ErrQueueFull is returned to a waiter that was evicted from the FIFO queue
// because newer requests pushed it out. Producers should treat it as a
// drop rather than a retryable failure.
var ErrQueueFull = errors.New("ratelimit: per-chat queue full, dropped")

// ErrLimiterClosed is returned when a producer races with limiter shutdown.
var ErrLimiterClosed = errors.New("ratelimit: limiter closed")

// Defaults intentionally separated as exported constants so callers can
// build their own limiter with custom values without re-deriving the
// numbers from the Telegram docs.
const (
	DefaultRate           = time.Second * 4    // one slot every 4 s -> 15/min
	DefaultQueueCapacity  = 50                 // per chat
	DefaultIdleTimeout    = 5 * time.Minute    // worker exit after idle
	DefaultReaperInterval = 1 * time.Minute    // background sweep cadence
)

// Limiter coordinates rate limiting for an arbitrary number of chats.
// Safe for concurrent use. Zero value is not usable - create with [New].
type Limiter struct {
	rate         time.Duration
	queueCap     int
	idleTimeout  time.Duration
	reaperEvery  time.Duration

	mu      sync.Mutex
	buckets map[int64]*chatBucket
	closed  bool
	log     *slog.Logger

	wg     sync.WaitGroup
	stopCh chan struct{}
}

// Config bundles all tunables; pass [Config{}] to inherit defaults.
type Config struct {
	Rate           time.Duration
	QueueCapacity  int
	IdleTimeout    time.Duration
	ReaperInterval time.Duration
	Logger         *slog.Logger
}

// New constructs a Limiter and starts the background reaper goroutine.
// Callers must invoke Close to release worker goroutines.
func New(cfg Config) *Limiter {
	if cfg.Rate <= 0 {
		cfg.Rate = DefaultRate
	}
	if cfg.QueueCapacity <= 0 {
		cfg.QueueCapacity = DefaultQueueCapacity
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = DefaultIdleTimeout
	}
	if cfg.ReaperInterval <= 0 {
		cfg.ReaperInterval = DefaultReaperInterval
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	l := &Limiter{
		rate:        cfg.Rate,
		queueCap:    cfg.QueueCapacity,
		idleTimeout: cfg.IdleTimeout,
		reaperEvery: cfg.ReaperInterval,
		buckets:     make(map[int64]*chatBucket),
		log:         cfg.Logger,
		stopCh:      make(chan struct{}),
	}
	l.wg.Add(1)
	go l.reaper()
	return l
}

// Wait blocks until the caller may proceed with one outgoing request to
// chatID, or returns an error if the queue is full, the context is
// canceled, or the limiter has been closed. On success the caller has
// consumed exactly one slot - it is up to the caller to do exactly one
// API call.
//
// chatID may be signed (Telegram-style) or absolute; the limiter treats
// them as opaque keys, but consumers should pick one form and stick to
// it (the wrapper uses absolute IDs).
func (l *Limiter) Wait(ctx context.Context, chatID int64) error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return ErrLimiterClosed
	}
	bucket := l.getOrCreateLocked(chatID)
	r := &request{ready: make(chan error, 1)}
	dropped := bucket.enqueueLocked(r, l.queueCap)
	bucket.ensureWorkerLocked(l)
	l.mu.Unlock()

	if dropped > 0 {
		l.log.Warn("ratelimit: dropped oldest queued request",
			"chat_id", chatID, "dropped", dropped, "queue_cap", l.queueCap)
	}

	select {
	case err := <-r.ready:
		return err
	case <-ctx.Done():
		// Best-effort: mark request canceled so the worker skips it. The
		// worker will still consume one slot if it has already started
		// servicing this request, but the caller will see ctx.Err either
		// way - that is the documented contract.
		r.cancel()
		return ctx.Err()
	}
}

// Close stops the reaper and all per-chat workers. After Close, all
// further Wait calls return ErrLimiterClosed and any waiting requests
// receive ErrLimiterClosed. Safe to call multiple times.
func (l *Limiter) Close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	close(l.stopCh)
	for _, b := range l.buckets {
		b.shutdown()
	}
	l.mu.Unlock()
	l.wg.Wait()
}

// ChatCount returns the number of currently tracked chats. Useful for
// tests to assert reaper behavior.
func (l *Limiter) ChatCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

func (l *Limiter) getOrCreateLocked(chatID int64) *chatBucket {
	b, ok := l.buckets[chatID]
	if ok {
		return b
	}
	b = &chatBucket{
		chatID: chatID,
		queue:  make([]*request, 0, l.queueCap),
		signal: make(chan struct{}, 1),
		stop:   make(chan struct{}),
	}
	l.buckets[chatID] = b
	return b
}

func (l *Limiter) deleteBucket(chatID int64, b *chatBucket) {
	l.mu.Lock()
	if cur, ok := l.buckets[chatID]; ok && cur == b {
		delete(l.buckets, chatID)
	}
	l.mu.Unlock()
}

func (l *Limiter) reaper() {
	defer l.wg.Done()
	t := time.NewTicker(l.reaperEvery)
	defer t.Stop()
	for {
		select {
		case <-l.stopCh:
			return
		case <-t.C:
			l.sweepIdle()
		}
	}
}

func (l *Limiter) sweepIdle() {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	for id, b := range l.buckets {
		b.mu.Lock()
		idle := len(b.queue) == 0 && !b.workerRunning &&
			!b.lastSeenAt.IsZero() && now.Sub(b.lastSeenAt) > l.idleTimeout
		b.mu.Unlock()
		if idle {
			delete(l.buckets, id)
		}
	}
}

// request is one queued operation. ready returns nil on slot grant or
// ErrQueueFull when evicted.
type request struct {
	mu       sync.Mutex
	ready    chan error
	canceled bool
}

func (r *request) cancel() {
	r.mu.Lock()
	r.canceled = true
	r.mu.Unlock()
}

func (r *request) isCanceled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.canceled
}

func (r *request) deliver(err error) {
	select {
	case r.ready <- err:
	default:
	}
}

type chatBucket struct {
	chatID int64

	mu              sync.Mutex
	queue           []*request
	signal          chan struct{} // buffered 1, used to wake the worker
	workerRunning   bool
	stop            chan struct{}
	nextAllowedAt   time.Time
	lastSeenAt      time.Time
}

// enqueueLocked appends r to the queue, evicting the oldest waiters with
// ErrQueueFull if the queue would exceed cap. Caller must hold l.mu (the
// Limiter mutex), which is enough because no worker reads queue without
// b.mu - and we take b.mu briefly here.
//
// Returns the number of dropped waiters so the caller can log.
func (b *chatBucket) enqueueLocked(r *request, cap int) int {
	b.mu.Lock()
	defer b.mu.Unlock()

	dropped := 0
	for len(b.queue) >= cap {
		old := b.queue[0]
		b.queue = b.queue[1:]
		old.deliver(ErrQueueFull)
		dropped++
	}
	b.queue = append(b.queue, r)
	b.lastSeenAt = time.Now()

	select {
	case b.signal <- struct{}{}:
	default:
	}
	return dropped
}

// ensureWorkerLocked starts the per-chat worker if it is not already
// running. Caller must hold l.mu so concurrent Wait calls do not race
// to spawn duplicate workers.
func (b *chatBucket) ensureWorkerLocked(l *Limiter) {
	b.mu.Lock()
	if b.workerRunning {
		b.mu.Unlock()
		return
	}
	b.workerRunning = true
	b.mu.Unlock()
	l.wg.Add(1)
	go b.run(l)
}

// shutdown signals the per-chat worker to exit and rejects all queued
// requests. Caller must hold l.mu.
func (b *chatBucket) shutdown() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, r := range b.queue {
		r.deliver(ErrLimiterClosed)
	}
	b.queue = nil
	select {
	case <-b.stop:
		// already closed
	default:
		close(b.stop)
	}
}

func (b *chatBucket) run(l *Limiter) {
	defer l.wg.Done()
	idleTimer := time.NewTimer(l.idleTimeout)
	defer idleTimer.Stop()

	for {
		// Drain one request, or wait for one.
		r, ok := b.popNext()
		if !ok {
			// No work. Wait for signal or idle timeout or stop.
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(l.idleTimeout)

			select {
			case <-l.stopCh:
				b.markStopped()
				return
			case <-b.stop:
				b.markStopped()
				return
			case <-b.signal:
				continue
			case <-idleTimer.C:
				if b.tryExitIfEmpty(l) {
					return
				}
				continue
			}
		}

		if r.isCanceled() {
			// Caller's ctx is done; do not consume a slot. Continue.
			continue
		}

		// Wait until next slot.
		now := time.Now()
		if b.nextAllowedAt.After(now) {
			wait := b.nextAllowedAt.Sub(now)
			select {
			case <-l.stopCh:
				r.deliver(ErrLimiterClosed)
				b.markStopped()
				return
			case <-b.stop:
				r.deliver(ErrLimiterClosed)
				b.markStopped()
				return
			case <-time.After(wait):
			}
			if r.isCanceled() {
				continue
			}
		}

		b.mu.Lock()
		next := time.Now().Add(l.rate)
		if next.After(b.nextAllowedAt) {
			b.nextAllowedAt = next
		}
		b.lastSeenAt = time.Now()
		b.mu.Unlock()

		r.deliver(nil)
	}
}

// popNext returns the head of the queue or (nil,false) if empty.
func (b *chatBucket) popNext() (*request, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.queue) == 0 {
		return nil, false
	}
	r := b.queue[0]
	b.queue = b.queue[1:]
	return r, true
}

func (b *chatBucket) markStopped() {
	b.mu.Lock()
	b.workerRunning = false
	for _, r := range b.queue {
		r.deliver(ErrLimiterClosed)
	}
	b.queue = nil
	b.mu.Unlock()
}

// tryExitIfEmpty exits the worker only if no work has accumulated since
// the timer fired - we re-check under lock to avoid losing a request.
func (b *chatBucket) tryExitIfEmpty(l *Limiter) bool {
	b.mu.Lock()
	if len(b.queue) > 0 {
		b.mu.Unlock()
		return false
	}
	b.workerRunning = false
	b.mu.Unlock()
	// Allow reaper to delete the bucket eventually.
	l.deleteBucket(b.chatID, b)
	return true
}
