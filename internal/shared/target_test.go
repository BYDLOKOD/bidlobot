package shared

import (
	"testing"

	"github.com/mymmrac/telego"
)

func TestResolveTargetFromReply(t *testing.T) {
	msg := &telego.Message{
		Text: "/warn Some reason here",
		ReplyToMessage: &telego.Message{
			From: &telego.User{ID: 222, Username: "bob", FirstName: "Bob"},
		},
	}

	target, reason, err := ResolveTarget(msg)
	if err != nil {
		t.Fatal(err)
	}
	if target.UserID != 222 {
		t.Fatalf("expected userID 222, got %d", target.UserID)
	}
	if reason != "Some reason here" {
		t.Fatalf("expected reason from text, got %q", reason)
	}
}

func TestResolveTargetFromUsername(t *testing.T) {
	msg := &telego.Message{
		Text: "/warn @bob Spam links",
	}

	target, reason, err := ResolveTarget(msg)
	if err != nil {
		t.Fatal(err)
	}
	if target.Username != "bob" {
		t.Fatalf("expected username bob, got %q", target.Username)
	}
	if reason != "Spam links" {
		t.Fatalf("expected 'Spam links', got %q", reason)
	}
}

func TestResolveTargetFromUserID(t *testing.T) {
	msg := &telego.Message{
		Text: "/warn 12345",
	}

	target, _, err := ResolveTarget(msg)
	if err != nil {
		t.Fatal(err)
	}
	if target.UserID != 12345 {
		t.Fatalf("expected userID 12345, got %d", target.UserID)
	}
}

func TestResolveTargetNoTarget(t *testing.T) {
	msg := &telego.Message{
		Text: "/warn",
	}

	_, _, err := ResolveTarget(msg)
	if err != ErrNoTarget {
		t.Fatalf("expected ErrNoTarget, got %v", err)
	}
}

func TestResolveTargetReplyPriority(t *testing.T) {
	msg := &telego.Message{
		Text: "/warn @alice Some reason",
		ReplyToMessage: &telego.Message{
			From: &telego.User{ID: 333, Username: "charlie"},
		},
	}

	target, reason, err := ResolveTarget(msg)
	if err != nil {
		t.Fatal(err)
	}
	if target.UserID != 333 {
		t.Fatal("reply should take priority over @username")
	}
	if reason != "@alice Some reason" {
		t.Fatalf("entire text after command should be reason when reply used, got %q", reason)
	}
}
