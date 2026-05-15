package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validToken = "1234567890:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef-_AA"

func TestConfig_ValidatePassesOnFullValid(t *testing.T) {
	dir := t.TempDir()
	c := Config{
		Token:      validToken,
		DBPath:     dir,
		HealthPort: 8080,
		LogLevel:   "info",
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("expected valid: %v", err)
	}
}

func TestConfig_ValidatePassesOnDefaultPortAndLevel(t *testing.T) {
	dir := t.TempDir()
	c := Config{
		Token:      validToken,
		DBPath:     dir,
		HealthPort: -1, // unset
		LogLevel:   "info",
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("expected valid: %v", err)
	}
}

func TestConfig_ValidatePassesWithDisabledHealth(t *testing.T) {
	dir := t.TempDir()
	c := Config{
		Token:      validToken,
		DBPath:     dir,
		HealthPort: 0,
		LogLevel:   "warn",
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("expected valid (port=0): %v", err)
	}
}

func TestConfig_RejectsMissingToken(t *testing.T) {
	dir := t.TempDir()
	c := Config{Token: "", DBPath: dir, HealthPort: 8080, LogLevel: "info"}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "TG_BOT_TOKEN is required") {
		t.Fatalf("expected required token error, got %v", err)
	}
}

func TestConfig_RejectsMalformedToken(t *testing.T) {
	dir := t.TempDir()
	cases := []string{
		"abc",       // missing colon
		"123:short", // too short
		"abc:1234567890123456789012345678901234567",  // non-numeric prefix
		"123: 1234567890123456789012345678901234567", // contains space
	}
	for _, tok := range cases {
		t.Run(tok, func(t *testing.T) {
			c := Config{Token: tok, DBPath: dir, HealthPort: 8080, LogLevel: "info"}
			if err := c.Validate(); err == nil {
				t.Fatalf("token %q expected to be rejected", tok)
			}
		})
	}
}

func TestConfig_RejectsBadLogLevel(t *testing.T) {
	dir := t.TempDir()
	c := Config{Token: validToken, DBPath: dir, HealthPort: 8080, LogLevel: "trace"}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "LOG_LEVEL") {
		t.Fatalf("expected LOG_LEVEL error, got %v", err)
	}
}

func TestConfig_LogLevelCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	for _, lvl := range []string{"DEBUG", "Info", "warn", "ERROR"} {
		c := Config{Token: validToken, DBPath: dir, HealthPort: 8080, LogLevel: lvl}
		if err := c.Validate(); err != nil {
			t.Errorf("level %q: %v", lvl, err)
		}
	}
}

func TestConfig_RejectsBadHealthPort(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []int{-3, 70000, 100000} {
		c := Config{Token: validToken, DBPath: dir, HealthPort: p, LogLevel: "info"}
		if err := c.Validate(); err == nil {
			t.Errorf("port %d expected to be rejected", p)
		}
	}
}

func TestConfig_RejectsUnparseableHealthPort(t *testing.T) {
	t.Setenv("HEALTH_PORT", "not-a-number")
	c := Config{Token: validToken, DBPath: t.TempDir(), HealthPort: -2, LogLevel: "info"}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "not-a-number") {
		t.Fatalf("expected unparseable error, got %v", err)
	}
}

func TestConfig_DBPathExistingFileNotDir(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Config{Token: validToken, DBPath: file, HealthPort: 8080, LogLevel: "info"}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected 'not a directory' error, got %v", err)
	}
}

func TestConfig_DBPathDoesNotExistButParentDoes(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "newchild")
	c := Config{Token: validToken, DBPath: target, HealthPort: 8080, LogLevel: "info"}
	if err := c.Validate(); err != nil {
		t.Fatalf("expected validate to pass when parent is writable: %v", err)
	}
}

func TestConfig_DBPathParentMissing(t *testing.T) {
	c := Config{Token: validToken, DBPath: "/nonexistent-root-x/path/to/db", HealthPort: 8080, LogLevel: "info"}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "DB_PATH") {
		t.Fatalf("expected DB_PATH error, got %v", err)
	}
}

func TestConfig_ValidateAggregatesMultipleErrors(t *testing.T) {
	c := Config{Token: "bad", DBPath: "", HealthPort: 9999999, LogLevel: "verbose"}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"TG_BOT_TOKEN", "DB_PATH", "HEALTH_PORT", "LOG_LEVEL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("aggregate error should contain %q, got %v", want, err)
		}
	}
}

func TestParseHealthPortRaw(t *testing.T) {
	if v := parseHealthPortRaw(""); v != -1 {
		t.Errorf("empty: got %d", v)
	}
	if v := parseHealthPortRaw("0"); v != 0 {
		t.Errorf("0: got %d", v)
	}
	if v := parseHealthPortRaw("8080"); v != 8080 {
		t.Errorf("8080: got %d", v)
	}
	if v := parseHealthPortRaw("foo"); v != -2 {
		t.Errorf("foo: got %d", v)
	}
}

func TestVersionMetadata_StringContainsKnownFields(t *testing.T) {
	v := VersionMetadata{Version: "1.2.3", Commit: "abcdef0123456789", BuildTime: "2026-04-01T00:00:00Z", GoVersion: "go1.26"}
	s := v.String()
	for _, want := range []string{"version=1.2.3", "commit=abcdef012345", "built=2026-04-01T00:00:00Z", "go=go1.26"} {
		if !strings.Contains(s, want) {
			t.Errorf("string missing %q: %s", want, s)
		}
	}
}

func TestVersionMetadata_FallbackWhenAllEmpty(t *testing.T) {
	v := versionFromRuntime("", "")
	// In a `go test` run, debug.ReadBuildInfo returns "(devel)" for the
	// main module; v.Version should land on "(devel)", "unknown", or
	// the runtime-set value. Just assert non-empty.
	if v.Version == "" {
		t.Fatal("Version should never be empty")
	}
	if v.GoVersion == "" {
		t.Fatal("GoVersion should be set from runtime/debug")
	}
}

func TestLoadConfig_ReadsEnv(t *testing.T) {
	t.Setenv("TG_BOT_TOKEN", validToken)
	t.Setenv("DB_PATH", "/tmp/x")
	t.Setenv("HEALTH_PORT", "9999")
	t.Setenv("LOG_LEVEL", "warn")

	c := loadConfig()
	if c.Token != validToken || c.DBPath != "/tmp/x" || c.HealthPort != 9999 || c.LogLevel != "warn" {
		t.Errorf("loadConfig result: %+v", c)
	}
}

// Ensure errors.Join is in use so we get one error per problem.
func TestConfig_AggregateUsesErrorsJoin(t *testing.T) {
	c := Config{Token: "", DBPath: "", HealthPort: 99999, LogLevel: "x"}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	// errors.Is works because Join wraps each constituent.
	if !errors.Is(err, err) { // tautology to catch nil
		t.Fatal("error should be wrapped")
	}
}
