package main

import (
	"strings"
	"testing"
)

// TestConfigRejectsMissingOwner verifies that BOT_OWNER_ID is a required
// field. A Config with zero BotOwnerID must fail validation.
func TestConfigRejectsMissingOwner(t *testing.T) {
	cfg := Config{
		Token:  validToken,
		DBPath: t.TempDir(),
		// BotOwnerID is omitted (zero value) - must be rejected
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Config with missing BOT_OWNER_ID must fail validation")
	}
	if !containsErr(err, "BOT_OWNER_ID") {
		t.Fatalf("validation error must mention BOT_OWNER_ID, got: %v", err)
	}
}

// TestConfigAcceptsValidOwner verifies that a Config with a valid
// BotOwnerID passes validation.
func TestConfigAcceptsValidOwner(t *testing.T) {
	cfg := Config{
		Token:      validToken,
		DBPath:     t.TempDir(),
		BotOwnerID: 123456789,
		HealthPort: 8080,
		LogLevel:   "info",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Config with valid BotOwnerID must pass validation: %v", err)
	}
}

// TestConfigRejectsNegativeOwner verifies that a negative BotOwnerID
// is rejected (user IDs are positive).
func TestConfigRejectsNegativeOwner(t *testing.T) {
	cfg := Config{
		Token:      validToken,
		DBPath:     t.TempDir(),
		BotOwnerID: -1,
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Config with negative BotOwnerID must fail validation")
	}
}

func containsErr(err error, substr string) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), substr)
}
