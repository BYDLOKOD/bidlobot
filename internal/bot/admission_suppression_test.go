package bot

import (
	"context"
	"errors"
	"testing"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/testutil"
)

type admissionAttemptCounter struct {
	counts map[int64]uint64
}

func (s *admissionAttemptCounter) RecordUnauthorizedAdmission(_ context.Context, userID int64) (uint64, error) {
	if s.counts == nil {
		s.counts = make(map[int64]uint64)
	}
	s.counts[userID]++
	return s.counts[userID], nil
}

type failingAdmissionAttemptStore struct{}

func (failingAdmissionAttemptStore) RecordUnauthorizedAdmission(context.Context, int64) (uint64, error) {
	return 0, errors.New("bolt unavailable")
}

func TestAdmissionNoticesStopAfterSecondAttempt(t *testing.T) {
	sender := testutil.NewMockAPI()
	leaver := &ownerChatsLeaver{}
	attempts := &admissionAttemptCounter{}
	a := &App{
		sender:     sender,
		leaver:     leaver,
		botOwnerID: 777,
	}
	a.SetAdmissionAttemptStore(attempts)
	handler := membershipMyChatMemberHandler(nil, a, testLogger())
	ctx := (&th.Context{}).WithContext(context.Background())
	cmu := telego.ChatMemberUpdated{
		Chat: telego.Chat{ID: -100111, Type: telego.ChatTypeSupergroup, Title: "Target"},
		From: telego.User{ID: 12345, FirstName: "Spammer", Username: "spammer"},
		OldChatMember: &telego.ChatMemberLeft{
			User: telego.User{ID: 999, IsBot: true},
		},
		NewChatMember: &telego.ChatMemberMember{
			User: telego.User{ID: 999, IsBot: true},
		},
	}

	for range 3 {
		if err := handler(ctx, cmu); err != nil {
			t.Fatal(err)
		}
	}
	if got := attempts.counts[12345]; got != 3 {
		t.Fatalf("attempt count = %d, want 3", got)
	}
	if len(leaver.calls) != 3 {
		t.Fatalf("LeaveChat calls = %d, want 3", len(leaver.calls))
	}
	if len(sender.Messages) != 2 {
		t.Fatalf("owner notices = %d, want 2", len(sender.Messages))
	}
}

func TestAdmissionCounterFailureSuppressesNotice(t *testing.T) {
	sender := testutil.NewMockAPI()
	leaver := &ownerChatsLeaver{}
	a := &App{
		sender:     sender,
		leaver:     leaver,
		botOwnerID: 777,
	}
	a.SetAdmissionAttemptStore(failingAdmissionAttemptStore{})
	handler := membershipMyChatMemberHandler(nil, a, testLogger())
	ctx := (&th.Context{}).WithContext(context.Background())
	cmu := telego.ChatMemberUpdated{
		Chat: telego.Chat{ID: -100111, Type: telego.ChatTypeSupergroup},
		From: telego.User{ID: 12345},
		OldChatMember: &telego.ChatMemberLeft{
			User: telego.User{ID: 999, IsBot: true},
		},
		NewChatMember: &telego.ChatMemberMember{
			User: telego.User{ID: 999, IsBot: true},
		},
	}

	if err := handler(ctx, cmu); err != nil {
		t.Fatal(err)
	}
	if len(leaver.calls) != 1 {
		t.Fatalf("LeaveChat calls = %d, want 1", len(leaver.calls))
	}
	if len(sender.Messages) != 0 {
		t.Fatalf("owner notices = %d, want 0", len(sender.Messages))
	}
}
