package bot

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
	"github.com/veschin/bidlobot/internal/testutil"
)

// chatMemberFromStatus returns the telego.ChatMember concrete type for a
// status string. Only the User field is populated; remaining type-specific
// fields use zero values, which is sufficient for MemberStatus() lookup.
func chatMemberFromStatus(user telego.User, status string) telego.ChatMember {
	switch status {
	case "creator":
		return &telego.ChatMemberOwner{User: user}
	case "administrator":
		return &telego.ChatMemberAdministrator{User: user}
	case "member":
		return &telego.ChatMemberMember{User: user}
	case "restricted":
		return &telego.ChatMemberRestricted{User: user}
	case "left":
		return &telego.ChatMemberLeft{User: user}
	case "kicked":
		return &telego.ChatMemberBanned{User: user}
	default:
		panic("unknown status: " + status)
	}
}

// makeCMU builds a ChatMemberUpdated for the given user as the bot, actor as
// the triggering user, with the given old/new status strings, in a supergroup.
func makeCMU(botUser, actor telego.User, oldStatus, newStatus string) telego.ChatMemberUpdated {
	return telego.ChatMemberUpdated{
		Chat:          telego.Chat{ID: -100, Type: telego.ChatTypeSupergroup},
		From:          actor,
		OldChatMember: chatMemberFromStatus(botUser, oldStatus),
		NewChatMember: chatMemberFromStatus(botUser, newStatus),
	}
}

// makeCMUInChat is like makeCMU but with an explicit chat type.
func makeCMUInChat(botUser, actor telego.User, oldStatus, newStatus string, chatType string) telego.ChatMemberUpdated {
	return telego.ChatMemberUpdated{
		Chat:          telego.Chat{ID: -100, Type: chatType},
		From:          actor,
		OldChatMember: chatMemberFromStatus(botUser, oldStatus),
		NewChatMember: chatMemberFromStatus(botUser, newStatus),
	}
}

func TestEvaluateMyChatMemberAdmission_NonOwnerAdd_Rejects(t *testing.T) {
	botUser := telego.User{ID: 999, IsBot: true}
	actor := telego.User{ID: 12345, IsBot: false}
	ownerID := int64(777)

	tests := []struct {
		name      string
		oldStatus string
		newStatus string
	}{
		{"old left -> new administrator", "left", "administrator"},
		{"old left -> new member", "left", "member"},
		{"old kicked -> new administrator", "kicked", "administrator"},
		{"old kicked -> new member", "kicked", "member"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmu := makeCMU(botUser, actor, tc.oldStatus, tc.newStatus)
			if got := EvaluateMyChatMemberAdmission(cmu, ownerID); got != AdmissionReject {
				t.Fatalf("EvaluateMyChatMemberAdmission() = %v, want AdmissionReject", got)
			}
		})
	}
}

func TestEvaluateMyChatMemberAdmission_OwnerAdd_Admits(t *testing.T) {
	botUser := telego.User{ID: 999, IsBot: true}
	ownerUser := telego.User{ID: 777, IsBot: false}
	ownerID := int64(777)

	tests := []struct {
		name      string
		oldStatus string
		newStatus string
	}{
		{"old left -> new administrator", "left", "administrator"},
		{"old left -> new member", "left", "member"},
		{"old kicked -> new administrator", "kicked", "administrator"},
		{"old kicked -> new member", "kicked", "member"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmu := makeCMU(botUser, ownerUser, tc.oldStatus, tc.newStatus)
			if got := EvaluateMyChatMemberAdmission(cmu, ownerID); got != AdmissionAdmit {
				t.Fatalf("EvaluateMyChatMemberAdmission() = %v, want AdmissionAdmit", got)
			}
		})
	}
}

func TestEvaluateMyChatMemberAdmission_NonSupergroup_Ignores(t *testing.T) {
	botUser := telego.User{ID: 999, IsBot: true}
	nonOwner := telego.User{ID: 12345, IsBot: false}
	ownerID := int64(777)

	chatTypes := []string{
		telego.ChatTypeGroup,
		telego.ChatTypePrivate,
		telego.ChatTypeChannel,
		telego.ChatTypeSender,
	}

	for _, chatType := range chatTypes {
		t.Run(chatType, func(t *testing.T) {
			cmu := makeCMUInChat(botUser, nonOwner, "left", "administrator", chatType)
			if got := EvaluateMyChatMemberAdmission(cmu, ownerID); got != AdmissionIgnore {
				t.Fatalf("EvaluateMyChatMemberAdmission(%s) = %v, want AdmissionIgnore", chatType, got)
			}
		})
	}
}

func TestEvaluateMyChatMemberAdmission_PromotionAndDemotion_Ignores(t *testing.T) {
	botUser := telego.User{ID: 999, IsBot: true}
	nonOwner := telego.User{ID: 12345, IsBot: false}
	ownerID := int64(777)

	tests := []struct {
		name      string
		oldStatus string
		newStatus string
	}{
		{"old admin -> new admin (re-promotion)", "administrator", "administrator"},
		{"old member -> new admin (promotion)", "member", "administrator"},
		{"old admin -> new member (demotion)", "administrator", "member"},
		{"old member -> new member", "member", "member"},
		{"old restricted -> new member", "restricted", "member"},
		{"old member -> new restricted", "member", "restricted"},
		{"old restricted -> new restricted", "restricted", "restricted"},
		{"old restricted -> new admin", "restricted", "administrator"},
		{"old creator -> new admin", "creator", "administrator"},
		{"old creator -> new member", "creator", "member"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmu := makeCMU(botUser, nonOwner, tc.oldStatus, tc.newStatus)
			if got := EvaluateMyChatMemberAdmission(cmu, ownerID); got != AdmissionIgnore {
				t.Fatalf("EvaluateMyChatMemberAdmission() = %v, want AdmissionIgnore", got)
			}
		})
	}
}

func TestEvaluateMyChatMemberAdmission_Departure_Ignores(t *testing.T) {
	botUser := telego.User{ID: 999, IsBot: true}
	nonOwner := telego.User{ID: 12345, IsBot: false}
	ownerID := int64(777)

	tests := []struct {
		name      string
		oldStatus string
		newStatus string
	}{
		{"old admin -> new left", "administrator", "left"},
		{"old member -> new left", "member", "left"},
		{"old admin -> new kicked", "administrator", "kicked"},
		{"old member -> new kicked", "member", "kicked"},
		{"old left -> new left", "left", "left"},
		{"old left -> new kicked", "left", "kicked"},
		{"old kicked -> new left", "kicked", "left"},
		{"old kicked -> new kicked", "kicked", "kicked"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmu := makeCMU(botUser, nonOwner, tc.oldStatus, tc.newStatus)
			if got := EvaluateMyChatMemberAdmission(cmu, ownerID); got != AdmissionIgnore {
				t.Fatalf("EvaluateMyChatMemberAdmission() = %v, want AdmissionIgnore", got)
			}
		})
	}
}

func TestEvaluateMyChatMemberAdmission_OwnerIDZero_Rejects(t *testing.T) {
	// When ownerID is 0 (unconfigured), no user ID can match, so all adds
	// are rejected.
	botUser := telego.User{ID: 999, IsBot: true}
	actor := telego.User{ID: 777, IsBot: false}
	ownerID := int64(0)

	cmu := makeCMU(botUser, actor, "left", "administrator")
	if got := EvaluateMyChatMemberAdmission(cmu, ownerID); got != AdmissionReject {
		t.Fatalf("EvaluateMyChatMemberAdmission() = %v, want AdmissionReject", got)
	}
}

func TestEvaluateMyChatMemberAdmission_NonSupergroupOwnerAdd_Ignores(t *testing.T) {
	// Even when the actor IS the owner, a non-supergroup chat is ignored.
	botUser := telego.User{ID: 999, IsBot: true}
	ownerUser := telego.User{ID: 777, IsBot: false}
	ownerID := int64(777)

	cmu := makeCMUInChat(botUser, ownerUser, "left", "member", telego.ChatTypeGroup)
	if got := EvaluateMyChatMemberAdmission(cmu, ownerID); got != AdmissionIgnore {
		t.Fatalf("EvaluateMyChatMemberAdmission() = %v, want AdmissionIgnore", got)
	}
}

func TestEvaluateMyChatMemberAdmission_NonAddNewStatus_Ignores(t *testing.T) {
	// Transitions from left/kicked to statuses other than member/admin are
	// not add transitions.
	botUser := telego.User{ID: 999, IsBot: true}
	nonOwner := telego.User{ID: 12345, IsBot: false}
	ownerID := int64(777)

	tests := []struct {
		oldStatus string
		newStatus string
	}{
		{"left", "restricted"},
		{"left", "creator"},
		{"kicked", "restricted"},
		{"kicked", "creator"},
	}

	for _, tc := range tests {
		t.Run(tc.oldStatus+"->"+tc.newStatus, func(t *testing.T) {
			cmu := makeCMU(botUser, nonOwner, tc.oldStatus, tc.newStatus)
			if got := EvaluateMyChatMemberAdmission(cmu, ownerID); got != AdmissionIgnore {
				t.Fatalf("EvaluateMyChatMemberAdmission() = %v, want AdmissionIgnore", got)
			}
		})
	}
}

func TestEvaluateMyChatMemberAdmission_ZeroBotID(t *testing.T) {
	// The bot user ID should not affect admission decisions.
	botUser := telego.User{ID: 0, IsBot: true}
	ownerUser := telego.User{ID: 777, IsBot: false}
	nonOwner := telego.User{ID: 12345, IsBot: false}
	ownerID := int64(777)

	t.Run("owner rejected with zero bot ID", func(t *testing.T) {
		cmu := makeCMU(botUser, ownerUser, "left", "administrator")
		if got := EvaluateMyChatMemberAdmission(cmu, ownerID); got != AdmissionAdmit {
			t.Fatalf("EvaluateMyChatMemberAdmission() = %v, want AdmissionAdmit", got)
		}
	})

	t.Run("non-owner rejected with zero bot ID", func(t *testing.T) {
		cmu := makeCMU(botUser, nonOwner, "left", "administrator")
		if got := EvaluateMyChatMemberAdmission(cmu, ownerID); got != AdmissionReject {
			t.Fatalf("EvaluateMyChatMemberAdmission() = %v, want AdmissionReject", got)
		}
	})
}

// mockLeaver records every LeaveChat call and returns a configured error.
type mockLeaver struct {
	mu    sync.Mutex
	calls []telego.ChatID
	err   error
}

func (m *mockLeaver) LeaveChat(_ context.Context, params *telego.LeaveChatParams) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, params.ChatID)
	return m.err
}

func (m *mockLeaver) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// assertAnError is a non-nil error for tests that expect a leave failure.
var assertAnError = errors.New("simulated leave failure")

// TestMyChatMemberHandler_NonOwnerAdd_LeavesAndDMsOwner verifies that the
// membershipMyChatMemberHandler calls LeaveChat on a non-owner add transition
// and, only on success, sends a best-effort owner DM through app.sender.
//
// RED: This test DOES NOT COMPILE because App has no "leaver" field of
// type ChatLeaver. The test documents the smallest injection seam:
//   - Add ChatLeaver interface to internal/shared
//   - Add leaver ChatLeaver field on App
//   - Wire app.bot.LeaveChat -> app.leaver.LeaveChat in membershipMyChatMemberHandler
//   - After successful LeaveChat, call app.sender.SendMessage to botOwnerID
//   - On LeaveChat failure, do not send any message, just log and return
func TestMyChatMemberHandler_NonOwnerAdd_LeavesAndDMsOwner(t *testing.T) {
	store, err := storage.NewBoltStore(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := membership.NewService(storage.NewMembershipRepo(store.DB()), testLogger())
	ts := time.Now().UTC().Unix()
	botUser := telego.User{ID: 999, IsBot: true}
	nonOwner := telego.User{ID: 12345, IsBot: false}

	t.Run("successful leave sends owner DM", func(t *testing.T) {
		mock := testutil.NewMockAPI()
		leaver := &mockLeaver{err: nil}
		a := &App{
			bot:        nil, // must not be used for leave
			sender:     mock,
			adminCache: shared.NewAdminCache(mock, 999, testLogger()),
			log:        testLogger(),
			memberSvc:  svc,
			botOwnerID: 777,
			leaver:     leaver, // FIELD DOES NOT EXIST - EXPECTED COMPILE ERROR
		}
		h := membershipMyChatMemberHandler(svc, a, testLogger())
		thctx := (&th.Context{}).WithContext(context.Background())

		cmu := telego.ChatMemberUpdated{
			Chat:          telego.Chat{ID: -100, Type: telego.ChatTypeSupergroup},
			Date:          ts,
			From:          nonOwner,
			OldChatMember: &telego.ChatMemberLeft{User: botUser},
			NewChatMember: &telego.ChatMemberMember{User: botUser},
		}

		before := len(mock.Messages)
		if err := h(thctx, cmu); err != nil {
			t.Fatalf("handler returned error: %v", err)
		}

		// LeaveChat was called exactly once
		if leaver.CallCount() != 1 {
			t.Fatalf("expected 1 LeaveChat call, got %d", leaver.CallCount())
		}

		// Owner DM was sent via app.sender (best-effort notification)
		if len(mock.Messages) != before+1 {
			t.Fatalf("expected 1 owner DM send on successful leave, got %d", len(mock.Messages)-before)
		}
		dm := mock.Messages[len(mock.Messages)-1]
		if dm.ChatID != 777 {
			t.Fatalf("owner DM should target owner ID 777, got %d", dm.ChatID)
		}
	})

	t.Run("failed leave does not send owner DM", func(t *testing.T) {
		mock := testutil.NewMockAPI()
		leaver := &mockLeaver{err: assertAnError}
		a := &App{
			bot:        nil,
			sender:     mock,
			adminCache: shared.NewAdminCache(mock, 999, testLogger()),
			log:        testLogger(),
			memberSvc:  svc,
			botOwnerID: 777,
			leaver:     leaver, // FIELD DOES NOT EXIST - EXPECTED COMPILE ERROR
		}
		h := membershipMyChatMemberHandler(svc, a, testLogger())
		thctx := (&th.Context{}).WithContext(context.Background())

		cmu := telego.ChatMemberUpdated{
			Chat:          telego.Chat{ID: -100, Type: telego.ChatTypeSupergroup},
			Date:          ts,
			From:          nonOwner,
			OldChatMember: &telego.ChatMemberLeft{User: botUser},
			NewChatMember: &telego.ChatMemberMember{User: botUser},
		}

		before := len(mock.Messages)
		if err := h(thctx, cmu); err != nil {
			t.Fatalf("handler returned error: %v", err)
		}
		// LeaveChat was still called once
		if leaver.CallCount() != 1 {
			t.Fatalf("expected 1 LeaveChat call, got %d", leaver.CallCount())
		}

		// No owner DM or any other message was sent
		if len(mock.Messages) != before {
			t.Fatalf("expected 0 sends when leave fails, got %d", len(mock.Messages)-before)
		}
	})
}
