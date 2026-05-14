package tgclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	"github.com/mymmrac/telego/telegoapi"

	"github.com/veschin/bidlobot/internal/shared/ratelimit"
	"github.com/veschin/bidlobot/internal/shared/retry"
)

// fakeMigrator records invocations.
type fakeMigrator struct {
	mu    sync.Mutex
	calls []migrationCall
	err   error
}

type migrationCall struct {
	OldAbs, NewAbs int64
}

func (f *fakeMigrator) MigrateChatID(_ context.Context, oldAbs, newAbs int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, migrationCall{oldAbs, newAbs})
	return f.err
}

type fakeAdmin struct {
	mu          sync.Mutex
	invalidated []int64
}

func (f *fakeAdmin) Invalidate(absChatID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invalidated = append(f.invalidated, absChatID)
}

func newClientForRunWrite(t *testing.T) (*Client, *fakeMigrator, *fakeAdmin, *ratelimit.Limiter) {
	t.Helper()
	limiter := ratelimit.New(ratelimit.Config{
		Rate:           1 * time.Millisecond,
		QueueCapacity:  16,
		IdleTimeout:    100 * time.Millisecond,
		ReaperInterval: 50 * time.Millisecond,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(func() { limiter.Close() })
	mig := &fakeMigrator{}
	adm := &fakeAdmin{}
	c := &Client{
		bot:         nil, // unused in runWrite tests
		limiter:     limiter,
		retryPolicy: retry.Policy{Sleep: func(ctx context.Context, d time.Duration) error { return nil }, Jitter: func(d time.Duration) time.Duration { return d }},
		migrator:    mig,
		admin:       adm,
		log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return c, mig, adm, limiter
}

func apiErr(code int, ra int) error {
	e := &telegoapi.Error{Description: "x", ErrorCode: code}
	if ra > 0 {
		e.Parameters = &telegoapi.ResponseParameters{RetryAfter: ra}
	}
	return fmt.Errorf("api: %w", e)
}

func migrateErr(newSigned int64) error {
	e := &telegoapi.Error{
		Description: "Bad Request: group chat was upgraded to a supergroup",
		ErrorCode:   400,
		Parameters:  &telegoapi.ResponseParameters{MigrateToChatID: newSigned},
	}
	return fmt.Errorf("api: %w", e)
}

// TestRunWrite_NoMigrationSimplePass verifies the happy path with no
// migration needed.
func TestRunWrite_NoMigrationSimplePass(t *testing.T) {
	c, mig, adm, _ := newClientForRunWrite(t)

	calls := 0
	err := c.runWrite(context.Background(), -1001, "test",
		func(ctx context.Context) error {
			calls++
			return nil
		},
		func(newSigned int64) { t.Fatalf("migrate not expected, got new=%d", newSigned) },
	)
	if err != nil {
		t.Fatalf("got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
	if len(mig.calls) != 0 {
		t.Errorf("expected no migrate calls, got %v", mig.calls)
	}
	if len(adm.invalidated) != 0 {
		t.Errorf("expected no admin invalidations, got %v", adm.invalidated)
	}
}

// TestRunWrite_MigrationRetriesWithNewID confirms the full flow:
// 1. first send returns 400 with migrate_to_chat_id
// 2. migrator runs with abs(old) -> abs(new)
// 3. admin cache invalidated for abs(old)
// 4. send retries with new chat id and succeeds
func TestRunWrite_MigrationRetriesWithNewID(t *testing.T) {
	c, mig, adm, _ := newClientForRunWrite(t)

	const oldSigned = int64(-1001234567890)
	const newSigned = int64(-1009876543210)
	currentChatID := oldSigned
	calls := 0

	err := c.runWrite(context.Background(), currentChatID, "sendMessage",
		func(ctx context.Context) error {
			calls++
			if currentChatID == oldSigned {
				return migrateErr(newSigned)
			}
			return nil
		},
		func(updated int64) { currentChatID = updated },
	)
	if err != nil {
		t.Fatalf("expected success after migration, got %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 send calls, got %d", calls)
	}
	if len(mig.calls) != 1 {
		t.Fatalf("expected 1 migration call, got %d", len(mig.calls))
	}
	wantOldAbs := int64(1001234567890)
	wantNewAbs := int64(1009876543210)
	if mig.calls[0] != (migrationCall{wantOldAbs, wantNewAbs}) {
		t.Errorf("migration call: %+v", mig.calls[0])
	}
	if len(adm.invalidated) != 1 || adm.invalidated[0] != wantOldAbs {
		t.Errorf("admin invalidation expected oldAbs %d, got %v", wantOldAbs, adm.invalidated)
	}
	if currentChatID != newSigned {
		t.Errorf("expected migrateApply to set new id %d, got %d", newSigned, currentChatID)
	}
}

// TestRunWrite_MigrationFailureSurfaces: if the migrator returns error,
// the entire call fails and no second send happens.
func TestRunWrite_MigrationFailureSurfaces(t *testing.T) {
	c, mig, _, _ := newClientForRunWrite(t)
	mig.err = errors.New("disk full")

	calls := 0
	err := c.runWrite(context.Background(), -111, "x",
		func(ctx context.Context) error {
			calls++
			return migrateErr(-222)
		},
		func(newSigned int64) {},
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("expected 1 call before migration failure, got %d", calls)
	}
}

// TestRunWrite_NonMigrationErrorPassesThrough: a plain 400 (not migration)
// is returned to the caller without invoking migrator.
func TestRunWrite_NonMigrationErrorPassesThrough(t *testing.T) {
	c, mig, adm, _ := newClientForRunWrite(t)

	err := c.runWrite(context.Background(), -1, "x",
		func(ctx context.Context) error { return apiErr(400, 0) },
		func(newSigned int64) {},
	)
	if err == nil {
		t.Fatal("expected error")
	}
	var apiE *telegoapi.Error
	if !errors.As(err, &apiE) || apiE.ErrorCode != 400 {
		t.Errorf("expected wrapped 400, got %v", err)
	}
	if len(mig.calls) != 0 {
		t.Errorf("migrator should not run, got %v", mig.calls)
	}
	if len(adm.invalidated) != 0 {
		t.Errorf("admin should not invalidate, got %v", adm.invalidated)
	}
}

// TestRunWrite_RetryThenMigrate: 5xx -> retry -> 400+migrate -> migrate +
// retry succeeds. Confirms retry runs inside the migration loop.
func TestRunWrite_RetryThenMigrate(t *testing.T) {
	c, mig, _, _ := newClientForRunWrite(t)

	const newSigned = int64(-2000)
	currentChatID := int64(-1000)
	calls := 0
	err := c.runWrite(context.Background(), currentChatID, "x",
		func(ctx context.Context) error {
			calls++
			switch calls {
			case 1:
				return apiErr(503, 0) // retried
			case 2:
				return migrateErr(newSigned)
			default:
				return nil
			}
		},
		func(updated int64) { currentChatID = updated },
	)
	if err != nil {
		t.Fatalf("got %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls (5xx, migrate, success), got %d", calls)
	}
	if len(mig.calls) != 1 {
		t.Errorf("expected 1 migration, got %v", mig.calls)
	}
}

// TestRunWrite_MigrationLoopBounded: a misbehaving server keeps redirecting;
// the wrapper should bail out after maxMigrations attempts.
func TestRunWrite_MigrationLoopBounded(t *testing.T) {
	c, _, _, _ := newClientForRunWrite(t)

	calls := 0
	err := c.runWrite(context.Background(), -1, "x",
		func(ctx context.Context) error {
			calls++
			return migrateErr(-1 - int64(calls)) // each call returns a different new id
		},
		func(newSigned int64) {},
	)
	if err == nil {
		t.Fatal("expected error")
	}
	// We cap at 2 migration iterations; each iteration calls send once.
	if calls > 4 {
		t.Errorf("expected bounded calls, got %d", calls)
	}
}

// TestRunWrite_ContextCancelDuringRateLimit: ctx cancel returns ctx.Err.
func TestRunWrite_ContextCancelDuringRateLimit(t *testing.T) {
	c, _, _, lim := newClientForRunWrite(t)
	// Saturate the bucket.
	if err := lim.Wait(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	// Now use a tight ctx.
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	err := c.runWrite(ctx, 7, "x",
		func(ctx context.Context) error {
			t.Fatal("send must not be called")
			return nil
		},
		func(newSigned int64) {},
	)
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Errorf("expected ctx error, got %v", err)
	}
}

// Compile-time interface assertion is verified at package level; this
// test ensures the New constructor catches missing deps.
func TestNewRejectsMissingDeps(t *testing.T) {
	bot := &telego.Bot{}
	limiter := ratelimit.New(ratelimit.Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	defer limiter.Close()

	tests := []struct {
		name string
		cfg  Config
	}{
		{"nil bot", Config{Limiter: limiter, Migrator: &fakeMigrator{}, Admin: &fakeAdmin{}}},
		{"nil limiter", Config{Bot: bot, Migrator: &fakeMigrator{}, Admin: &fakeAdmin{}}},
		{"nil migrator", Config{Bot: bot, Limiter: limiter, Admin: &fakeAdmin{}}},
		{"nil admin", Config{Bot: bot, Limiter: limiter, Migrator: &fakeMigrator{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.cfg); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
