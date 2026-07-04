package bot

import (
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

