package bot

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
	"github.com/veschin/bidlobot/internal/testutil"
	"github.com/veschin/bidlobot/internal/text"
)

// TestMyChatMemberHandlerRoutesAllSendsThroughSender is a regression
// guard for the audited rate-limiter-bypass defect. Every public send
// in membershipMyChatMemberHandler must go through App.sender (the
// rate-limited tgclient wrapper), never the raw *telego.Bot. The
// "member" branch (bot added or demoted to non-admin) is the bot's
// first-contact path for many chats; routing it through the raw bot
// reintroduced the 20 msg/min/chat flood the audit removed.
//
// The App is built with sender set and bot left nil: any branch that
// still used app.bot dereferences nil and panics, so this test both
// reproduces the prior bug (panic) and locks the fix (recorded send).
func TestMyChatMemberHandlerRoutesAllSendsThroughSender(t *testing.T) {
	store, err := storage.NewBoltStore(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := membership.NewService(storage.NewMembershipRepo(store.DB()), testLogger())

	mock := testutil.NewMockAPI()
	a := &App{
		sender:     mock,
		adminCache: shared.NewAdminCache(mock, 999, testLogger()),
		log:        testLogger(),
	}
	h := membershipMyChatMemberHandler(svc, a, testLogger())
	thctx := (&th.Context{}).WithContext(context.Background())
	ts := time.Now().UTC().Unix()
	botUser := telego.User{ID: 999, IsBot: true}

	cases := []struct {
		name string
		cmu  telego.ChatMemberUpdated
		want string
	}{
		{
			name: "member branch sends need-admin via sender",
			cmu: telego.ChatMemberUpdated{
				Chat:          telego.Chat{ID: -100, Type: telego.ChatTypeSupergroup},
				Date:          ts,
				OldChatMember: &telego.ChatMemberAdministrator{User: botUser},
				NewChatMember: &telego.ChatMemberMember{User: botUser},
			},
			want: text.MsgNeedAdmin,
		},
		{
			name: "newly promoted admin sends onboarding via sender",
			cmu: telego.ChatMemberUpdated{
				Chat:          telego.Chat{ID: -100, Type: telego.ChatTypeSupergroup},
				Date:          ts,
				OldChatMember: &telego.ChatMemberMember{User: botUser},
				NewChatMember: &telego.ChatMemberAdministrator{User: botUser, CanRestrictMembers: true},
			},
			want: msgOnboardingAdmin,
		},
		{
			name: "non-supergroup sends upgrade notice via sender",
			cmu: telego.ChatMemberUpdated{
				Chat:          telego.Chat{ID: -1, Type: telego.ChatTypeGroup},
				Date:          ts,
				OldChatMember: &telego.ChatMemberMember{User: botUser},
				NewChatMember: &telego.ChatMemberMember{User: botUser},
			},
			want: text.MsgNotSupergroup,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(mock.Messages)
			if err := h(thctx, tc.cmu); err != nil {
				t.Fatalf("handler returned error: %v", err)
			}
			if len(mock.Messages) != before+1 {
				t.Fatalf("expected exactly one send through App.sender, got %d",
					len(mock.Messages)-before)
			}
			got := mock.Messages[len(mock.Messages)-1]
			if got.Text != tc.want {
				t.Fatalf("text mismatch:\n got  %q\n want %q", got.Text, tc.want)
			}
		})
	}
}
