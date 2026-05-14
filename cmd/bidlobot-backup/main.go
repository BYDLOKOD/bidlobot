// bidlobot-backup creates a consistent point-in-time copy of the bbolt
// database while the main bot is running. Uses the documented bbolt
// hot-copy approach: a read transaction snapshots the freelist + page
// view, then [bbolt.Tx.WriteTo] streams the snapshot to disk.
//
// We deliberately use a separate binary rather than a shell-only file
// copy because bbolt's mmap'd file is not safe to copy with cp/dd while
// writes are in flight - a torn page could leave the backup corrupt.
// The Go API guarantees a stable view from the moment Begin returns.
//
// Output path defaults to backups/bidlobot-YYYYMMDD-HHMMSS.db relative
// to the cwd; use -out to override.
//
// The accompanying scripts/backup.sh handles flock and rotation.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

func main() {
	var (
		src     = flag.String("src", "data/bidlobot.db", "path to source bbolt database")
		out     = flag.String("out", "", "destination file (default: backups/bidlobot-<ts>.db)")
		timeout = flag.Duration("timeout", 5*time.Second, "bolt open timeout")
	)
	flag.Parse()

	if err := run(*src, *out, *timeout); err != nil {
		fmt.Fprintln(os.Stderr, "backup:", err)
		os.Exit(1)
	}
}

func run(src, out string, timeout time.Duration) error {
	if src == "" {
		return fmt.Errorf("-src is required")
	}
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("source: %w", err)
	}

	if out == "" {
		ts := time.Now().UTC().Format("20060102-150405")
		out = filepath.Join("backups", "bidlobot-"+ts+".db")
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return fmt.Errorf("create destination dir: %w", err)
	}

	// Open read-only with a generous timeout. The main bot writes
	// frequently; we want to wait briefly for the lock rather than fail
	// outright if a write is in progress.
	db, err := bolt.Open(src, 0o600, &bolt.Options{
		ReadOnly: true,
		Timeout:  timeout,
	})
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer db.Close()

	// Write to a temp file then atomically rename. Avoids leaving a
	// half-written backup on disk if the snapshot fails midway.
	tmp, err := os.CreateTemp(filepath.Dir(out), filepath.Base(out)+".part-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op if rename succeeds

	var bytesWritten int64
	err = db.View(func(tx *bolt.Tx) error {
		n, werr := tx.WriteTo(tmp)
		bytesWritten = n
		return werr
	})
	if cerr := tmp.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}

	if err := os.Rename(tmpPath, out); err != nil {
		return fmt.Errorf("publish: %w", err)
	}

	fmt.Printf("backup: wrote %d bytes to %s\n", bytesWritten, out)
	return nil
}
