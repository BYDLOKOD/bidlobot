package bot

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/storage"
	"github.com/veschin/bidlobot/internal/testutil"
)

type ownerChatsLeaver struct {
	calls []int64
	err   error
}

func (l *ownerChatsLeaver) LeaveChat(_ context.Context, params *telego.LeaveChatParams) error {
	l.calls = append(l.calls, params.ChatID.ID)
	return l.err
}

func newOwnerChatsTestApp(t *testing.T) (*App, *storage.MembershipRepo, *testutil.MockAPI, *ownerChatsLeaver) {
	t.Helper()
	store, err := storage.NewBoltStore(filepath.Join(t.TempDir(), "owner-chats.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	repo := storage.NewMembershipRepo(store.DB())
	sender := testutil.NewMockAPI()
	leaver := &ownerChatsLeaver{}
	a := &App{
		sender:     sender,
		leaver:     leaver,
		memberSvc:  membership.NewService(repo, testLogger()),
		botOwnerID: 777,
		log:        testLogger(),
	}
	return a, repo, sender, leaver
}

func TestOwnerChatsListShowsOnlyCurrentChatsWithRevokeButtons(t *testing.T) {
	a, repo, sender, _ := newOwnerChatsTestApp(t)
	now := time.Now().UTC()
	for _, chat := range []membership.Chat{
		{AbsChatID: 100111, Title: "Active chat", Type: telego.ChatTypeSupergroup, BotStatus: membership.StatusAdministrator, LastUpdateAt: now},
		{AbsChatID: 100222, Title: "Departed chat", Type: telego.ChatTypeSupergroup, BotStatus: membership.StatusLeft, LastUpdateAt: now},
	} {
		if err := repo.UpsertChat(context.Background(), chat); err != nil {
			t.Fatal(err)
		}
	}

	msg := telego.Message{
		Chat: telego.Chat{ID: 777, Type: telego.ChatTypePrivate},
		From: &telego.User{ID: 777},
	}
	if err := a.handleOwnerChats(nil, msg); err != nil {
		t.Fatal(err)
	}

	got := sender.LastMessage()
	if got == nil {
		t.Fatal("expected chat list message")
	}
	if !strings.Contains(got.Text, "Active chat") || strings.Contains(got.Text, "Departed chat") {
		t.Fatalf("unexpected chat list: %q", got.Text)
	}
	if got.Keyboard == nil || len(got.Keyboard.InlineKeyboard) != 1 {
		t.Fatalf("expected one revoke button, got %#v", got.Keyboard)
	}
	button := got.Keyboard.InlineKeyboard[0][0]
	if button.CallbackData != "oc:ask:100111" {
		t.Fatalf("callback data = %q, want confirmation step", button.CallbackData)
	}
}

func TestOwnerChatsRevokeRequiresOwnerConfirmation(t *testing.T) {
	a, repo, sender, leaver := newOwnerChatsTestApp(t)
	now := time.Now().UTC()
	if err := repo.UpsertChat(context.Background(), membership.Chat{
		AbsChatID:    100111,
		Title:        "Active chat",
		Type:         telego.ChatTypeSupergroup,
		BotStatus:    membership.StatusAdministrator,
		CanDelete:    true,
		LastUpdateAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	privateMessage := &telego.Message{
		MessageID: 42,
		Chat:      telego.Chat{ID: 777, Type: telego.ChatTypePrivate},
	}
	forged := telego.CallbackQuery{
		ID:      "forged",
		From:    telego.User{ID: 888},
		Message: privateMessage,
		Data:    "oc:leave:100111",
	}
	if err := a.handleOwnerChatsCallback(nil, forged); err != nil {
		t.Fatal(err)
	}
	if len(leaver.calls) != 0 {
		t.Fatal("non-owner callback must not revoke the bot")
	}

	ask := telego.CallbackQuery{
		ID:      "ask",
		From:    telego.User{ID: 777},
		Message: privateMessage,
		Data:    "oc:ask:100111",
	}
	if err := a.handleOwnerChatsCallback(nil, ask); err != nil {
		t.Fatal(err)
	}
	confirm := lastEdit(t, sender)
	if !strings.Contains(confirm.Text, "Подтвердить отзыв") {
		t.Fatalf("confirmation text = %q", confirm.Text)
	}
	if confirm.ReplyMarkup == nil || confirm.ReplyMarkup.InlineKeyboard[0][0].CallbackData != "oc:leave:100111" {
		t.Fatalf("missing final confirmation button: %#v", confirm.ReplyMarkup)
	}

	leave := ask
	leave.ID = "leave"
	leave.Data = "oc:leave:100111"
	if err := a.handleOwnerChatsCallback(nil, leave); err != nil {
		t.Fatal(err)
	}
	if len(leaver.calls) != 1 || leaver.calls[0] != -100111 {
		t.Fatalf("leave calls = %v, want [-100111]", leaver.calls)
	}
	stored, err := repo.GetChat(context.Background(), 100111)
	if err != nil {
		t.Fatal(err)
	}
	if stored.BotStatus != membership.StatusLeft || stored.CanDelete {
		t.Fatalf("stored chat after revoke = %#v", stored)
	}
	if got := lastEdit(t, sender).Text; !strings.Contains(got, "Бот вышел из чата") {
		t.Fatalf("completion text = %q", got)
	}
}

func TestOwnerChatsFailedLeaveKeepsChatActive(t *testing.T) {
	a, repo, _, leaver := newOwnerChatsTestApp(t)
	leaver.err = errors.New("telegram unavailable")
	if err := repo.UpsertChat(context.Background(), membership.Chat{
		AbsChatID:    100111,
		Title:        "Active chat",
		Type:         telego.ChatTypeSupergroup,
		BotStatus:    membership.StatusMember,
		LastUpdateAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	query := telego.CallbackQuery{
		ID:   "leave",
		From: telego.User{ID: 777},
		Message: &telego.Message{
			MessageID: 42,
			Chat:      telego.Chat{ID: 777, Type: telego.ChatTypePrivate},
		},
		Data: "oc:leave:100111",
	}
	if err := a.handleOwnerChatsCallback(nil, query); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetChat(context.Background(), 100111)
	if err != nil {
		t.Fatal(err)
	}
	if stored.BotStatus != membership.StatusMember {
		t.Fatalf("failed leave changed status to %q", stored.BotStatus)
	}
}

func lastEdit(t *testing.T, sender *testutil.MockAPI) *telego.EditMessageTextParams {
	t.Helper()
	for i := len(sender.Calls) - 1; i >= 0; i-- {
		if sender.Calls[i].Method == "EditMessageText" {
			params, ok := sender.Calls[i].Params.(*telego.EditMessageTextParams)
			if !ok {
				t.Fatalf("EditMessageText params type = %T", sender.Calls[i].Params)
			}
			return params
		}
	}
	t.Fatal("expected EditMessageText call")
	return nil
}
