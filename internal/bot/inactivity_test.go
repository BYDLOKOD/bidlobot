package bot

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/storage"
	"github.com/veschin/bidlobot/internal/testutil"
)

const inactivityTestChat = 100123

func TestInactivityUsesPersistedMessageHistory(t *testing.T) {
	ctx := context.Background()
	store, err := storage.NewBoltStore(filepath.Join(t.TempDir(), "activity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := storage.NewMembershipRepo(store.DB())
	svc := membership.NewService(repo)
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	for _, record := range []struct {
		id       int64
		username string
		age      time.Duration
		status   membership.Status
	}{
		{1, "boundary", 48 * time.Hour, membership.StatusMember},
		{2, "included", 48*time.Hour + time.Nanosecond, membership.StatusMember},
		{3, "week", 7 * 24 * time.Hour, membership.StatusMember},
		{4, "month", 30 * 24 * time.Hour, membership.StatusMember},
		{5, "bot", 31 * 24 * time.Hour, membership.StatusMember},
		{6, "departed", 31 * 24 * time.Hour, membership.StatusLeft},
		{7, "banned", 31 * 24 * time.Hour, membership.StatusKicked},
		{8, "silent", 0, membership.StatusMember},
	} {
		if record.age != 0 {
			from := telego.User{ID: record.id, Username: record.username, IsBot: record.id == 5}
			if err := svc.RecordMessage(ctx, inactivityTestChat, &from, now.Add(-record.age)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := repo.UpsertMember(ctx, membership.MemberPatch{
			UserID: record.id, AbsChatID: inactivityTestChat, Status: record.status, Username: &record.username,
		}); err != nil {
			t.Fatal(err)
		}
	}
	m := newInactivityMenu(nil, repo, store.DB())
	assertPage := func(hours int, want string) {
		t.Helper()
		entries, err := m.inactiveMembers(ctx, inactivityTestChat, hours, now)
		if err != nil {
			t.Fatal(err)
		}
		got, page, more := inactivityPage(entries, now, 0)
		if got != want || page != 0 || more {
			t.Fatalf("page=%d more=%t alert=%q; want %q", page, more, got, want)
		}
	}
	assertPage(48, "included: 1 сообщ. · 7д: нет · 30д: нет\nweek: 1 сообщ. · 7д: да · 30д: нет\nmonth: 1 сообщ. · 7д: да · 30д: да")
	if err := svc.RecordMessage(ctx, inactivityTestChat, &telego.User{ID: 4, Username: "month"}, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	assertPage(48, "included: 1 сообщ. · 7д: нет · 30д: нет\nweek: 1 сообщ. · 7д: да · 30д: нет")
	assertPage(1, "boundary: 1 сообщ. · 7д: нет · 30д: нет\nincluded: 1 сообщ. · 7д: нет · 30д: нет\nweek: 1 сообщ. · 7д: да · 30д: нет\nmonth: 2 сообщ. · 7д: нет · 30д: нет")
}

func TestInactivityPrivateInlineBatchesAndFreshAuthorization(t *testing.T) {
	ctx := context.Background()
	store, err := storage.NewBoltStore(filepath.Join(t.TempDir(), "inline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := storage.NewMembershipRepo(store.DB())
	api := &inactivityTestAPI{MockAPI: testutil.NewMockAPI()}
	m := newInactivityMenu(api, repo, store.DB())
	for _, chat := range []membership.Chat{
		{AbsChatID: inactivityTestChat, Title: "Protected Source", Type: telego.ChatTypeSupergroup, BotStatus: membership.StatusMember},
		{AbsChatID: inactivityTestChat + 1, Title: "Protected Other", Type: telego.ChatTypeSupergroup, BotStatus: membership.StatusMember},
	} {
		if err := repo.UpsertChat(ctx, chat); err != nil {
			t.Fatal(err)
		}
	}
	api.ChatMembers["100123:7"] = telego.MemberStatusCreator
	api.ChatMembers["100123:8"] = telego.MemberStatusAdministrator
	first := strings.Repeat("🦉", 64)
	second := strings.Repeat("Щ", 64)
	longPrefix := "Длинное имя "
	third := longPrefix + strings.Repeat("🦉", 150)
	svc := membership.NewService(repo)
	for i, name := range []string{first, second, third} {
		if err := svc.RecordMessage(ctx, inactivityTestChat, &telego.User{ID: int64(i + 1), FirstName: name}, time.Now().Add(-32*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	result, handled := m.results(ctx, telego.InlineQuery{From: telego.User{ID: 7}, Query: "inactive Protected", ChatType: telego.ChatTypePrivate})
	if !handled || len(result) != 1 {
		t.Fatalf("expected one authorized chat, got handled=%t results=%v", handled, result)
	}
	article := result[0].(*telego.InlineQueryResultArticle)
	if article.Title != "Protected Source" {
		t.Fatalf("expected private source title, got %q", article.Title)
	}
	assertGeneric := func(value any) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		for _, private := range []string{"Protected", first, second, longPrefix, "100123"} {
			if strings.Contains(string(data), private) {
				t.Fatalf("shared output contains %q: %s", private, data)
			}
		}
	}
	assertGeneric(article.InputMessageContent)
	assertGeneric(article.ReplyMarkup)
	click := func(actor int64, data string) {
		t.Helper()
		api.alert = telego.AnswerCallbackQueryParams{}
		if err := m.callback(ctx, telego.CallbackQuery{
			ID: "callback", From: telego.User{ID: actor}, InlineMessageID: "same-inline-message", Data: data,
		}); err != nil {
			t.Fatal(err)
		}
		if api.alert.CallbackQueryID != "callback" {
			t.Fatalf("callback not answered: %#v", api.alert)
		}
	}
	launch := article.ReplyMarkup.InlineKeyboard[0][0].CallbackData
	click(8, launch)
	if api.edit != nil {
		t.Fatal("another admin used the actor's launcher")
	}
	click(7, launch)
	if api.edit == nil || api.edit.InlineMessageID != "same-inline-message" || api.edit.ChatID.ID != 0 || api.edit.MessageID != 0 {
		t.Fatalf("expected edit of the same inline message, got %#v", api.edit)
	}
	list := inactivityTestButton(t, api.edit, "Список")
	assertGeneric(api.edit)
	click(7, list)
	assertAlert := func(name string) {
		t.Helper()
		if !api.alert.ShowAlert || api.alert.CacheTime != 0 || !strings.Contains(api.alert.Text, name) || len(utf16.Encode([]rune(api.alert.Text))) > 200 {
			t.Fatalf("invalid private alert: %#v", api.alert)
		}
		assertGeneric(api.edit)
	}
	assertAlert(first)
	click(7, inactivityTestButton(t, api.edit, "Далее"))
	assertAlert(second)
	click(7, inactivityTestButton(t, api.edit, "Далее"))
	assertAlert(longPrefix)
	if !strings.HasSuffix(api.alert.Text, "...: 1 сообщ. · 7д: да · 30д: да") {
		t.Fatalf("truncation removed message count or inactivity flags: %q", api.alert.Text)
	}
	click(7, inactivityTestButton(t, api.edit, "Назад"))
	assertAlert(second)
	click(7, inactivityTestButton(t, api.edit, "Назад"))
	assertAlert(first)
	previous := api.edit
	click(8, list)
	if api.edit != previous || strings.Contains(api.alert.Text, first) {
		t.Fatal("another admin used the actor's list")
	}
	api.ChatMembers["100123:7"] = telego.MemberStatusMember
	click(7, list)
	if api.edit != previous || strings.Contains(api.alert.Text, first) || strings.Contains(api.alert.Text, second) {
		t.Fatal("stale permission exposed or changed the menu")
	}
}

func TestInactivityIntervalPersistsPerChat(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "settings.db")
	store, err := storage.NewBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	repo := storage.NewMembershipRepo(store.DB())
	if err := repo.UpsertChat(ctx, membership.Chat{
		AbsChatID: inactivityTestChat, Type: telego.ChatTypeSupergroup, BotStatus: membership.StatusMember,
	}); err != nil {
		t.Fatal(err)
	}
	api := &inactivityTestAPI{MockAPI: testutil.NewMockAPI()}
	api.ChatMembers["100123:7"] = telego.MemberStatusAdministrator
	m := newInactivityMenu(api, repo, store.DB())
	var cb telego.CallbackQuery
	cb.ID, cb.From.ID, cb.InlineMessageID = "callback", 7, "same-inline-message"
	result, _ := m.results(ctx, telego.InlineQuery{From: cb.From, Query: "inactive"})
	if len(result) != 1 {
		t.Fatalf("expected one source, got %v", result)
	}
	click := func(data string) {
		t.Helper()
		cb.Data = data
		if err := m.callback(ctx, cb); err != nil {
			t.Fatal(err)
		}
	}
	click(result[0].(*telego.InlineQueryResultArticle).ReplyMarkup.InlineKeyboard[0][0].CallbackData)
	if !strings.Contains(api.edit.Text, "48 ч") {
		t.Fatalf("default threshold missing: %q", api.edit.Text)
	}
	click(inactivityTestButton(t, api.edit, "Порог"))
	click(inactivityTestButton(t, api.edit, "+24 ч"))
	if !strings.Contains(api.edit.Text, "72 ч") {
		t.Fatalf("changed threshold missing: %q", api.edit.Text)
	}
	api.ChatMembers["100123:7"] = telego.MemberStatusMember
	click(inactivityTestButton(t, api.edit, "+24 ч"))
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = storage.NewBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	m = newInactivityMenu(api, storage.NewMembershipRepo(store.DB()), store.DB())
	for chat, want := range map[int64]int{inactivityTestChat: 72, inactivityTestChat + 1: 48} {
		got, err := m.interval(chat)
		if err != nil || got != want {
			t.Fatalf("chat %d: interval=%d err=%v, want %d", chat, got, err, want)
		}
	}
}

type inactivityTestAPI struct {
	*testutil.MockAPI
	alert telego.AnswerCallbackQueryParams
	edit  *telego.EditMessageTextParams
}

func (api *inactivityTestAPI) AnswerCallbackQuery(_ context.Context, p *telego.AnswerCallbackQueryParams) error {
	api.alert = *p
	return nil
}

func (api *inactivityTestAPI) EditMessageText(_ context.Context, p *telego.EditMessageTextParams) (*telego.Message, error) {
	api.edit = p
	return nil, nil
}

func inactivityTestButton(t *testing.T, edit *telego.EditMessageTextParams, label string) string {
	t.Helper()
	if edit != nil && edit.ReplyMarkup != nil {
		for _, row := range edit.ReplyMarkup.InlineKeyboard {
			for _, b := range row {
				if b.Text == label && b.CallbackData != "" && len(b.CallbackData) <= 64 {
					return b.CallbackData
				}
			}
		}
	}
	t.Fatalf("expected callback button %q in %#v", label, edit)
	return ""
}
