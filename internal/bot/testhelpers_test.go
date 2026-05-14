package bot

import (
	"errors"
	"log/slog"
	"os"
)

// testLogger returns a slog.Logger that only emits ERROR-level entries
// to stderr. Tests use it so the production loggers stay quiet during
// the test run.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// fixedAdminChecker is a deterministic AdminChecker for tests. The
// boolean returned by IsAdmin is the same for every call.
type fixedAdminChecker struct {
	allow bool
	err   error
}

func (c *fixedAdminChecker) IsAdmin(_, _ int64) (bool, error) {
	if c.err != nil {
		return false, c.err
	}
	return c.allow, nil
}

// stubAdminCache helper keeps test bodies short.
func stubAdminCache(allow bool) AdminChecker {
	return &fixedAdminChecker{allow: allow}
}

// failingAdminCache returns the supplied error from every IsAdmin call.
// Currently unused but kept for upcoming Phase 3d/3e tests where we
// want to assert the dispatcher logs a warning and denies.
var _ = func(err error) AdminChecker {
	return &fixedAdminChecker{err: errors.New("not used yet")}
}
