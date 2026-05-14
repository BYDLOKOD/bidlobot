package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
)

// Config bundles every operator-supplied input. Loading from env is split
// from validation so tests can inject Config{} literals.
type Config struct {
	Token     string
	DBPath    string
	HealthPort int    // 0 disables; -1 means "unset, use default"
	LogLevel  string // debug|info|warn|error
}

// loadConfig reads Config from environment without performing validation.
// Validation happens in [Config.Validate] so the operator can also call
// it via --check-config.
func loadConfig() Config {
	return Config{
		Token:      os.Getenv("TG_BOT_TOKEN"),
		DBPath:     envOr("DB_PATH", "./data"),
		HealthPort: parseHealthPortRaw(os.Getenv("HEALTH_PORT")),
		LogLevel:   envOr("LOG_LEVEL", "info"),
	}
}

// parseHealthPortRaw parses HEALTH_PORT into an int with -1 sentinel for
// "unset". Returns -2 for "set but unparseable" so the caller can flag
// the validation error.
func parseHealthPortRaw(raw string) int {
	if raw == "" {
		return -1
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return -2
	}
	return v
}

// tokenRegexp mirrors telego's accepted token format: numeric bot id,
// colon, then 35+ chars of [A-Za-z0-9_-].
//
// We validate up-front so a typo in a deployment env file fails fast at
// startup rather than producing a confusing 401 from Telegram on the
// first GetMe.
var tokenRegexp = regexp.MustCompile(`^\d+:[A-Za-z0-9_-]{35,}$`)

// Validate returns nil when c is internally consistent. Errors are joined
// so --check-config can dump every problem in one run rather than fixing
// one at a time.
func (c Config) Validate() error {
	var errs []error

	if c.Token == "" {
		errs = append(errs, errors.New("TG_BOT_TOKEN is required"))
	} else if !tokenRegexp.MatchString(c.Token) {
		errs = append(errs, errors.New("TG_BOT_TOKEN: format must match `\\d+:[A-Za-z0-9_-]{35,}`"))
	}

	if c.DBPath == "" {
		errs = append(errs, errors.New("DB_PATH is empty"))
	} else if err := validateDBPath(c.DBPath); err != nil {
		errs = append(errs, fmt.Errorf("DB_PATH: %w", err))
	}

	switch c.HealthPort {
	case -1, 0:
		// -1 = unset (use default 8080); 0 = explicit disable. Both ok.
	case -2:
		errs = append(errs, fmt.Errorf("HEALTH_PORT: not an integer: %q", os.Getenv("HEALTH_PORT")))
	default:
		if c.HealthPort < 1 || c.HealthPort > 65535 {
			errs = append(errs, fmt.Errorf("HEALTH_PORT: out of range 1..65535: %d", c.HealthPort))
		}
	}

	switch strings.ToLower(c.LogLevel) {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("LOG_LEVEL: must be one of debug|info|warn|error, got %q", c.LogLevel))
	}

	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}

// validateDBPath ensures DBPath exists and is writable, or that its
// parent exists and is writable so MkdirAll can succeed.
func validateDBPath(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve absolute: %w", err)
	}
	info, err := os.Stat(abs)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%s exists and is not a directory", abs)
		}
		return checkWritable(abs)
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", abs, err)
	}
	parent := filepath.Dir(abs)
	if pinfo, perr := os.Stat(parent); perr == nil && pinfo.IsDir() {
		return checkWritable(parent)
	}
	return fmt.Errorf("neither %s nor its parent %s exists", abs, parent)
}

// checkWritable creates and removes a temporary marker to prove that
// the bot will be able to open data/bidlobot.db.
func checkWritable(dir string) error {
	probe, err := os.CreateTemp(dir, ".bidlobot-write-probe-*")
	if err != nil {
		return fmt.Errorf("write probe in %s: %w", dir, err)
	}
	name := probe.Name()
	probe.Close()
	if err := os.Remove(name); err != nil {
		// Treat unable to delete as warning - the file exists which means
		// we can write, but we have left litter. Log via the returned
		// err so the operator notices.
		return fmt.Errorf("write probe %s: created but could not remove: %w", name, err)
	}
	return nil
}

// VersionMetadata holds the build identity used by --version and
// /version. version/commit are typically populated via -ldflags
// "-X main.version=... -X main.commit=...". When unset we fall back
// to runtime/debug.ReadBuildInfo, which works for `go install` builds
// from a clean checkout.
type VersionMetadata struct {
	Version   string
	Commit    string
	BuildTime string
	GoVersion string
}

// versionFromRuntime reads runtime/debug.ReadBuildInfo and merges it
// with optional ldflags overrides.
func versionFromRuntime(version, commit string) VersionMetadata {
	v := VersionMetadata{
		Version: version,
		Commit:  commit,
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		v.GoVersion = info.GoVersion
		if v.Version == "" || v.Version == "(devel)" {
			v.Version = info.Main.Version
		}
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				if v.Commit == "" {
					v.Commit = s.Value
				}
			case "vcs.time":
				v.BuildTime = s.Value
			}
		}
	}
	if v.Version == "" {
		v.Version = "unknown"
	}
	return v
}

// String formats the metadata as a single human-readable banner suitable
// for --version output.
func (v VersionMetadata) String() string {
	parts := []string{"bidlobot version=" + v.Version}
	if v.Commit != "" {
		short := v.Commit
		if len(short) > 12 {
			short = short[:12]
		}
		parts = append(parts, "commit="+short)
	}
	if v.BuildTime != "" {
		parts = append(parts, "built="+v.BuildTime)
	}
	if v.GoVersion != "" {
		parts = append(parts, "go="+v.GoVersion)
	}
	return strings.Join(parts, " ")
}
