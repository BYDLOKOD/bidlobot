package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	"github.com/mymmrac/telego/telegoapi"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/domain/stats"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/shared/ratelimit"
	"github.com/veschin/bidlobot/internal/shared/tgclient"
	"github.com/veschin/bidlobot/internal/storage"
)

type inlineWireCaller func(string, []byte) (*telegoapi.Response, error)

func (f inlineWireCaller) Call(_ context.Context, url string, data *telegoapi.RequestData) (*telegoapi.Response, error) {
	return f(filepath.Base(url), data.BodyRaw)
}

func TestNativeMemberMenusEditOriginalInlineMessage(t *testing.T) {
	ctx := context.Background()
	store, err := storage.NewBoltStore(filepath.Join(t.TempDir(), "menus.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	members := storage.NewMembershipRepo(store.DB())
	const chatID int64 = -1001234567890
	const chatTitle = "Private source group"
	if err := members.UpsertChat(ctx, membership.Chat{
		AbsChatID: -chatID, Title: chatTitle, Type: telego.ChatTypeSupergroup, BotStatus: membership.StatusAdministrator,
	}); err != nil {
		t.Fatal(err)
	}
	memberSvc := membership.NewService(members)
	actor := telego.User{ID: 42, FirstName: "Admin"}
	if err := memberSvc.RecordMessage(ctx, -chatID, &telego.User{ID: 7, Username: "inactive_member"}, time.Now().Add(-35*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	var results []struct {
		Type        string                         `json:"type"`
		ReplyMarkup telego.InlineKeyboardMarkup    `json:"reply_markup"`
		Content     telego.InputTextMessageContent `json:"input_message_content"`
	}
	var edits []telego.EditMessageTextParams
	var answers []telego.AnswerCallbackQueryParams
	caller := inlineWireCaller(func(method string, body []byte) (*telegoapi.Response, error) {
		result := json.RawMessage(`true`)
		switch method {
		case "answerInlineQuery":
			var params struct {
				Results    json.RawMessage `json:"results"`
				CacheTime  *int            `json:"cache_time"`
				IsPersonal bool            `json:"is_personal"`
			}
			if err := json.Unmarshal(body, &params); err != nil {
				return nil, err
			}
			if !params.IsPersonal {
				return nil, fmt.Errorf("inline results are not personal")
			}
			if params.CacheTime == nil || *params.CacheTime != 0 {
				return nil, fmt.Errorf("inline answers must explicitly disable caching")
			}
			if err := json.Unmarshal(params.Results, &results); err != nil {
				return nil, err
			}
			for _, result := range results {
				if result.Type != telego.ResultTypeArticle {
					return nil, fmt.Errorf("invalid inline result type %q", result.Type)
				}
			}
		case "getChatMember":
			var params telego.GetChatMemberParams
			if err := json.Unmarshal(body, &params); err != nil {
				return nil, err
			}
			if params.ChatID.ID != chatID || params.UserID != actor.ID {
				return nil, fmt.Errorf("unexpected membership lookup: %+v", params)
			}
			result = json.RawMessage(`{"status":"creator","user":{"id":42,"first_name":"Admin"}}`)
		case "editMessageText":
			var params telego.EditMessageTextParams
			if err := json.Unmarshal(body, &params); err != nil {
				return nil, err
			}
			edits = append(edits, params)
		case "answerCallbackQuery":
			var params telego.AnswerCallbackQueryParams
			if err := json.Unmarshal(body, &params); err != nil {
				return nil, err
			}
			answers = append(answers, params)
		default:
			return nil, fmt.Errorf("unexpected Telegram method %q", method)
		}
		return &telegoapi.Response{Ok: true, Result: result}, nil
	})
	const fakeToken = "123456:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	tgBot, err := telego.NewBot(fakeToken, telego.WithAPICaller(caller), telego.WithRequestConstructor(InlineRequestConstructor{}))
	if err != nil {
		t.Fatal(err)
	}
	log := testLogger()
	admin := shared.NewAdminCache(tgBot, 999, log)
	limiter := ratelimit.New(ratelimit.Config{Rate: time.Hour, Logger: log})
	t.Cleanup(limiter.Close)
	if err := limiter.Wait(ctx, 0); err != nil {
		t.Fatal(err)
	}
	sender, err := tgclient.New(tgclient.Config{Bot: tgBot, Limiter: limiter, Migrator: store, Admin: admin, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	a := NewApp(tgBot, sender, log, admin, nil, nil, memberSvc, NewInlineService(log))
	a.AttachMemberMenus(store.DB())
	h, err := th.NewBotHandler(tgBot, nil)
	if err != nil {
		t.Fatal(err)
	}
	registerRoutes(h, a, &stats.Handler{})
	dispatch := func(t *testing.T, u telego.Update) {
		t.Helper()
		ctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if err := h.BaseGroup().HandleUpdate(ctx, tgBot, u); err != nil {
			t.Fatal(err)
		}
	}

	for _, feature := range []string{"inactive", "professions"} {
		t.Run(feature, func(t *testing.T) {
			results, edits, answers = nil, nil, nil
			dispatch(t, telego.Update{InlineQuery: &telego.InlineQuery{
				ID: "inline-query", From: actor, Query: feature, ChatType: telego.ChatTypeSupergroup,
			}})
			if len(results) == 0 || len(results[0].ReplyMarkup.InlineKeyboard) == 0 || len(results[0].ReplyMarkup.InlineKeyboard[0]) == 0 {
				t.Fatalf("%s did not return a native menu with a button", feature)
			}
			if strings.HasPrefix(results[0].Content.MessageText, "/") {
				t.Fatal("menu result sent a slash command")
			}
			messageID := "inline-message-" + feature
			click := func(id, data string) {
				t.Helper()
				dispatch(t, telego.Update{CallbackQuery: &telego.CallbackQuery{
					ID: id, From: actor, Data: data, InlineMessageID: messageID, ChatInstance: "shared-destination",
				}})
			}
			click("open", results[0].ReplyMarkup.InlineKeyboard[0][0].CallbackData)
			if len(edits) != 1 || edits[0].InlineMessageID != messageID || edits[0].ChatID.ID != 0 || edits[0].MessageID != 0 {
				t.Fatalf("expected one edit to %q, got %+v", messageID, edits)
			}
			if len(answers) != 1 || answers[0].CallbackQueryID != "open" {
				t.Fatalf("expected callback acknowledgement, got %+v", answers)
			}
			if strings.Contains(edits[0].Text, chatTitle) || strings.Contains(edits[0].Text, "inactive_member") {
				t.Fatal("shared menu disclosed chat or participant identity")
			}
			if feature == "inactive" {
				click("page", edits[0].ReplyMarkup.InlineKeyboard[0][0].CallbackData)
				if len(answers) != 2 || !answers[1].ShowAlert || !strings.Contains(answers[1].Text, "inactive_member") {
					t.Fatalf("expected a private inactivity page, got %+v", answers)
				}
				if len(edits) != 2 || edits[1].InlineMessageID != messageID || strings.Contains(edits[1].Text, "inactive_member") {
					t.Fatalf("inactivity page leaked or replaced the original menu: %+v", edits)
				}
			}
		})
	}
}
