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
	"time"

	"github.com/veschin/bidlobot/internal/domain/cleanup"
)

// Config bundles every operator-supplied input. Loading from env is split
// from validation so tests can inject Config{} literals.
type Config struct {
	Token      string
	DBPath     string
	HealthPort int    // 0 disables; -1 means "unset, use default"
	LogLevel   string // debug|info|warn|error

	// --- inactive-cleanup campaign tuning ---
	//
	// The campaign is started per-chat by the DM `/cleanup` command, not
	// by env. These only tune how the daily public lifecycle paces once
	// an admin has started it. Raw strings kept beside parsed values so
	// --check-config can report a bad value precisely.
	CleanupDailyAtRaw string // "HH:MM" UTC - when the daily tick fires
	CleanupDailyAtMin int    // minutes past 00:00 UTC; -1 = unparseable
	CleanupGraceRaw   string // tag->kick delay, e.g. "72h"
	CleanupGrace      time.Duration
	CleanupDailyBatch int // max members tagged per chat per daily run

	// Chat summarization via Pi/OMP with DeepSeek V4 Flash. The Pi
	// binary must be on $PATH and the model selector is fully qualified.
	// PIBinary defaults to "omp"; PIModel defaults to
	// "deepseek/deepseek-v4-flash". The bot always starts with summarization
	// enabled (the Pi binary is validated at startup); no API keys are
	// accepted from BidloBot config - the Pi CLI carries its own credential.
	PIBinary string
	PIModel  string

	// Required: the Telegram user id of the bot owner. Only this user may
	// add the bot to a supergroup; any non-owner add triggers an immediate
	// LeaveChat. Must be a positive int64. Zero rejects all admission.
	BotOwnerID int64

	// Captcha for new chat members (opt-in). When CaptchaEnabled is false
	// (the default) the bot runs exactly as before - no join messages, no
	// buttons, no sweep. CaptchaTimeoutRaw is kept beside the parsed
	// CaptchaTimeout so --check-config can report a bad value precisely.
	CaptchaEnabled    bool
	CaptchaTimeoutRaw string
	CaptchaTimeout    time.Duration
}

// loadConfig reads Config from environment without performing validation.
// Validation happens in [Config.Validate] so the operator can also call
// it via --check-config.
func loadConfig() Config {
	atRaw := envOr("CLEANUP_DAILY_AT", "10:00")
	graceRaw := envOr("CLEANUP_GRACE", "72h")

	grace, _ := cleanup.ParsePeriod(graceRaw)

	captchaTimeoutRaw := envOr("CAPTCHA_TIMEOUT", "1m")
	captchaTimeout, _ := time.ParseDuration(captchaTimeoutRaw)

	return Config{
		Token:      os.Getenv("TG_BOT_TOKEN"),
		DBPath:     envOr("DB_PATH", "./data"),
		HealthPort: parseHealthPortRaw(os.Getenv("HEALTH_PORT")),
		LogLevel:   envOr("LOG_LEVEL", "info"),

		CleanupDailyAtRaw: atRaw,
		CleanupDailyAtMin: parseHHMM(atRaw),
		CleanupGraceRaw:   graceRaw,
		CleanupGrace:      grace,
		CleanupDailyBatch: envInt("CLEANUP_DAILY_BATCH", 15),

		PIBinary: envOr("PI_BINARY", "omp"),
		PIModel:  envOr("PI_MODEL", "deepseek/deepseek-v4-flash"),

		BotOwnerID: parseOwnerID(os.Getenv("BOT_OWNER_ID")),

		CaptchaEnabled:    envBool("CAPTCHA_ENABLED", false),
		CaptchaTimeoutRaw: captchaTimeoutRaw,
		CaptchaTimeout:    captchaTimeout,
	}
}

// parseOwnerID parses BOT_OWNER_ID as an int64. A non-positive or
// unparseable value returns 0 so Validate can flag it uniformly.
func parseOwnerID(raw string) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || v <= 0 {
		return 0
	}
	return v
}

// parseHHMM turns "HH:MM" (24h, UTC) into minutes past midnight, or -1
// when malformed / out of range.
func parseHHMM(s string) int {
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return -1
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return -1
	}
	return hh*60 + mm
}

func envBool(key string, fallback bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	switch v {
	case "":
		return fallback
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func envInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
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

	// The campaign is command-driven (no enable flag); the daily
	// scheduler is always live, so these tunables are always validated.
	// They have safe defaults, so an error only fires when the operator
	// set a bad value explicitly.
	// Zero / empty means "unset" - safe defaults apply downstream
	// (gracekick.Config.normalized + AttachDailyCleanup). Only an
	// explicitly-supplied bad value is an error.
	if c.CleanupDailyAtMin < 0 {
		errs = append(errs, fmt.Errorf("CLEANUP_DAILY_AT: must be HH:MM 24h UTC, got %q", c.CleanupDailyAtRaw))
	}
	if c.CleanupGraceRaw != "" && (c.CleanupGrace < time.Hour || c.CleanupGrace > 30*24*time.Hour) {
		errs = append(errs, fmt.Errorf("CLEANUP_GRACE: must parse to 1h..720h, got %q", c.CleanupGraceRaw))
	}
	if c.CleanupDailyBatch != 0 && (c.CleanupDailyBatch < 1 || c.CleanupDailyBatch > 50) {
		errs = append(errs, fmt.Errorf("CLEANUP_DAILY_BATCH: must be 1..50, got %d", c.CleanupDailyBatch))
	}
	if c.CaptchaEnabled {
		// CaptchaTimeout is parsed in loadConfig; a zero value means the
		// raw string did not parse. Range 1m..30m: below 1m a human
		// can't answer, above 30m the mute window is needlessly long.
		switch {
		case c.CaptchaTimeout <= 0:
			errs = append(errs, fmt.Errorf("CAPTCHA_TIMEOUT: invalid duration %q", c.CaptchaTimeoutRaw))
		case c.CaptchaTimeout < time.Minute || c.CaptchaTimeout > 30*time.Minute:
			errs = append(errs, fmt.Errorf("CAPTCHA_TIMEOUT: must be 1m..30m, got %q", c.CaptchaTimeoutRaw))
		}
	}

	if c.BotOwnerID <= 0 {
		errs = append(errs, errors.New("BOT_OWNER_ID is required: positive int64 Telegram user ID"))
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
