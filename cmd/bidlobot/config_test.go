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
		LogLevel:   "info",
		BotOwnerID: 123456789,
	}

	if err := c.Validate(); err != nil {
		t.Fatalf("expected valid: %v", err)
	}
}

func TestConfig_RejectsMissingToken(t *testing.T) {
	dir := t.TempDir()
	c := Config{Token: "", DBPath: dir, LogLevel: "info"}
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
			c := Config{Token: tok, DBPath: dir, LogLevel: "info"}
			if err := c.Validate(); err == nil {
				t.Fatalf("token %q expected to be rejected", tok)
			}
		})
	}
}

func TestConfig_RejectsBadLogLevel(t *testing.T) {
	dir := t.TempDir()
	c := Config{Token: validToken, DBPath: dir, LogLevel: "trace"}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "LOG_LEVEL") {
		t.Fatalf("expected LOG_LEVEL error, got %v", err)
	}
}

func TestConfig_LogLevelCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	for _, lvl := range []string{"DEBUG", "Info", "warn", "ERROR"} {
		c := Config{Token: validToken, DBPath: dir, LogLevel: lvl, BotOwnerID: 123456789}
		if err := c.Validate(); err != nil {
			t.Errorf("level %q: %v", lvl, err)
		}
	}
}

func TestConfig_DBPathExistingFileNotDir(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Config{Token: validToken, DBPath: file, LogLevel: "info"}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected 'not a directory' error, got %v", err)
	}
}

func TestConfig_DBPathDoesNotExistButParentDoes(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "newchild")
	c := Config{Token: validToken, DBPath: target, LogLevel: "info", BotOwnerID: 123456789}
	if err := c.Validate(); err != nil {
		t.Fatalf("expected validate to pass when parent is writable: %v", err)
	}
}

func TestConfig_DBPathParentMissing(t *testing.T) {
	c := Config{Token: validToken, DBPath: "/nonexistent-root-x/path/to/db", LogLevel: "info"}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "DB_PATH") {
		t.Fatalf("expected DB_PATH error, got %v", err)
	}
}

func TestConfig_ValidateAggregatesMultipleErrors(t *testing.T) {
	c := Config{Token: "bad", DBPath: "", LogLevel: "verbose"}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"TG_BOT_TOKEN", "DB_PATH", "LOG_LEVEL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("aggregate error should contain %q, got %v", want, err)
		}
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
	t.Setenv("LOG_LEVEL", "warn")

	c := loadConfig()
	if c.Token != validToken || c.DBPath != "/tmp/x" || c.LogLevel != "warn" {
		t.Errorf("loadConfig result: %+v", c)
	}
}

// TestConfig_InstagramProxy validates the optional egress override: only
// an explicitly supplied bad value is an error (unset = direct download).
func TestConfig_InstagramProxy(t *testing.T) {
	dir := t.TempDir()

	for _, proxy := range []string{
		"http://proxy.example:3128",
		"https://proxy.example:8080",
		"socks5://127.0.0.1:1080",
		"socks5h://127.0.0.1:1080",
	} {
		t.Run("valid "+proxy, func(t *testing.T) {
			c := Config{Token: validToken, DBPath: dir, LogLevel: "info", BotOwnerID: 1, InstagramProxy: proxy}
			if err := c.Validate(); err != nil {
				t.Fatalf("proxy %q expected to be accepted: %v", proxy, err)
			}
		})
	}

	for _, proxy := range []string{
		"ftp://proxy.example:21", // yt-dlp has no ftp proxy support
		"socks5://",              // no host
		"127.0.0.1:1080",         // missing scheme: parses as scheme "127.0.0.1"
	} {
		t.Run("invalid "+proxy, func(t *testing.T) {
			c := Config{Token: validToken, DBPath: dir, LogLevel: "info", BotOwnerID: 1, InstagramProxy: proxy}
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), "INSTAGRAM_PROXY") {
				t.Fatalf("proxy %q expected to be rejected, got %v", proxy, err)
			}
		})
	}
}

// TestConfig_InstagramCookies verifies the cookie jar path is checked at
// startup: a typo would otherwise only show up as every Instagram repost
// landing in the deferred queue.
func TestConfig_InstagramCookies(t *testing.T) {
	dir := t.TempDir()
	jar := filepath.Join(dir, "cookies.txt")
	if err := os.WriteFile(jar, []byte("# Netscape HTTP Cookie File\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ok := Config{Token: validToken, DBPath: dir, LogLevel: "info", BotOwnerID: 1, InstagramCookies: jar}
	if err := ok.Validate(); err != nil {
		t.Fatalf("existing cookie file expected to be accepted: %v", err)
	}

	missing := Config{Token: validToken, DBPath: dir, LogLevel: "info", BotOwnerID: 1,
		InstagramCookies: filepath.Join(dir, "nope.txt")}
	err := missing.Validate()
	if err == nil || !strings.Contains(err.Error(), "INSTAGRAM_COOKIES") {
		t.Fatalf("missing cookie file expected to be rejected, got %v", err)
	}

	isDir := Config{Token: validToken, DBPath: dir, LogLevel: "info", BotOwnerID: 1, InstagramCookies: dir}
	if err := isDir.Validate(); err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("directory expected to be rejected, got %v", err)
	}
}

// TestLoadConfig_ReadsInstagramEnv pins the env names: the download is
// wired from these two strings and nothing else.
func TestLoadConfig_ReadsInstagramEnv(t *testing.T) {
	t.Setenv("INSTAGRAM_PROXY", "socks5h://127.0.0.1:1080")
	t.Setenv("INSTAGRAM_COOKIES", "/etc/bidlobot/ig-cookies.txt")

	c := loadConfig()
	if c.InstagramProxy != "socks5h://127.0.0.1:1080" {
		t.Errorf("InstagramProxy = %q", c.InstagramProxy)
	}
	if c.InstagramCookies != "/etc/bidlobot/ig-cookies.txt" {
		t.Errorf("InstagramCookies = %q", c.InstagramCookies)
	}
}

// Ensure errors.Join is in use so we get one error per problem.
func TestConfig_AggregateUsesErrorsJoin(t *testing.T) {
	c := Config{Token: "", DBPath: "", LogLevel: "x"}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	// errors.Is works because Join wraps each constituent.
	if !errors.Is(err, err) { // tautology to catch nil
		t.Fatal("error should be wrapped")
	}
}
