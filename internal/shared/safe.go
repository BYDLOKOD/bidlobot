package shared

import (
	"log/slog"
	"runtime/debug"
)

// Go runs fn in a goroutine and turns a panic into a log entry. A bare
// `go fn()` dies with the process on panic: no recover exists on that
// goroutine's stack.
func Go(log *slog.Logger, name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error("background goroutine panic recovered",
					"name", name, "panic", r, "stack", string(debug.Stack()))
			}
		}()
		fn()
	}()
}
