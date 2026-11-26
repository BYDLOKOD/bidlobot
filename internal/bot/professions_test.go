package bot

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/storage"
	"github.com/veschin/bidlobot/internal/testutil"
)

type professionsTest struct {
	t     *testing.T
	menu  *professionsMenu
	api   *testutil.MockAPI
	board string
}

func newProfessionsTest(t *testing.T) (*professionsTest, func()) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "professions.db")
	open := func() *storage.BoltStore {
		s, err := storage.NewBoltStore(path)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	store := open()
	t.Cleanup(func() { _ = store.Close() })
	api := testutil.NewMockAPI()
	repo := storage.NewMembershipRepo(store.DB())
	for _, chat := range []membership.Chat{
		{AbsChatID: 100101, Title: "Первая группа", Type: telego.ChatTypeSupergroup, BotStatus: membership.StatusAdministrator},
		{AbsChatID: 100102, Title: "Вторая группа", Type: telego.ChatTypeSupergroup, BotStatus: membership.StatusAdministrator},
	} {
		if err := repo.UpsertChat(ctx, chat); err != nil {
			t.Fatal(err)
		}
	}
	for userID, username := range map[int64]string{7: "alice", 8: "bob_42", 9: "alice"} {
		if _, err := repo.UpsertMember(ctx, membership.MemberPatch{AbsChatID: 100101, UserID: userID, Username: &username}); err != nil {
			t.Fatal(err)
		}
	}
	api.ChatMembers["100101:1"] = telego.MemberStatusAdministrator
	api.ChatMembers["100102:2"] = telego.MemberStatusLeft
	api.ChatMemberUsers["100101:7"] = telego.User{Username: "alice", FirstName: "Алиса"}
	api.ChatMemberUsers["100101:8"] = telego.User{Username: "bob_42", FirstName: "Борис"}
	api.ChatMemberUsers["100101:9"] = telego.User{Username: "previous_alice", FirstName: "Тёзка"}
	p := &professionsTest{t: t, menu: newProfessionsMenu(api, repo, store.DB()), api: api, board: "original-inline-message"}
	return p, func() {
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		store = open()
		p.menu = newProfessionsMenu(api, storage.NewMembershipRepo(store.DB()), store.DB())
	}
}

func (p *professionsTest) article(actor int64, query string) *telego.InlineQueryResultArticle {
	p.t.Helper()
	results, handled := p.menu.results(context.Background(), telego.InlineQuery{From: telego.User{ID: actor}, Query: query})
	if !handled || len(results) != 1 {
		p.t.Fatalf("query %q: handled=%v, results=%+v", query, handled, results)
	}
	article := results[0].(*telego.InlineQueryResultArticle)
	if article.Title != "Реестр профессий" {
		p.t.Fatalf("unexpected registry title: %q", article.Title)
	}
	return article
}

func (p *professionsTest) press(actor int64, button telego.InlineKeyboardButton) (*telego.InlineKeyboardMarkup, string) {
	p.t.Helper()
	start := len(p.api.Calls)
	if err := p.menu.callback(context.Background(), telego.CallbackQuery{ID: "callback", From: telego.User{ID: actor}, InlineMessageID: p.board, Data: button.CallbackData}); err != nil {
		p.t.Fatal(err)
	}
	var keyboard *telego.InlineKeyboardMarkup
	var answer string
	var answered bool
	for _, call := range p.api.Calls[start:] {
		switch args := call.Params.(type) {
		case *telego.EditMessageTextParams:
			if args.InlineMessageID != p.board || args.MessageID != 0 || args.ChatID.ID != 0 {
				p.t.Fatalf("updated wrong message: %+v", args)
			}
			if strings.Contains(args.Text, "alice") || strings.Contains(args.Text, "Аналитик") || strings.Contains(args.Text, "Первая группа") {
				p.t.Fatalf("private data appeared in shared edit: %q", args.Text)
			}
			for _, row := range args.ReplyMarkup.InlineKeyboard {
				for _, button := range row {
					if strings.Contains(button.Text, "alice") || strings.Contains(button.Text, "Аналитик") || strings.Contains(button.Text, "Первая группа") {
						p.t.Fatalf("private data appeared on shared button: %q", button.Text)
					}
				}
			}
			keyboard = args.ReplyMarkup
		case *telego.AnswerCallbackQueryParams:
			if answered || args.CallbackQueryID != "callback" || args.Text != "" && !args.ShowAlert {
				p.t.Fatalf("unexpected callback answer: %+v", args)
			}
			answered = true
			answer = args.Text
		case *telego.SendMessageParams:
			p.t.Fatal("sent an extra message instead of updating inline menu")
		}
	}
	if !answered {
		p.t.Fatal("callback not answered")
	}
	return keyboard, answer
}

func professionsButton(t *testing.T, kb *telego.InlineKeyboardMarkup, text string) telego.InlineKeyboardButton {
	t.Helper()
	if kb != nil {
		for _, row := range kb.InlineKeyboard {
			for _, button := range row {
				if button.Text == text {
					return button
				}
			}
		}
	}
	t.Fatalf("button %q missing from %+v", text, kb)
	return telego.InlineKeyboardButton{}
}

func (p *professionsTest) open(actor int64, choice string) (*telego.InlineKeyboardMarkup, string) {
	p.t.Helper()
	article := p.article(actor, "professions")
	kb, _ := p.press(actor, professionsButton(p.t, article.ReplyMarkup, "Выбрать чат"))
	return p.press(actor, professionsButton(p.t, kb, choice))
}

func (p *professionsTest) start(kb *telego.InlineKeyboardMarkup, action string) (*telego.InlineKeyboardMarkup, string) {
	p.t.Helper()
	kb, _ = p.press(1, professionsButton(p.t, kb, action))
	entry := professionsButton(p.t, kb, "Ввести текст")
	if entry.SwitchInlineQueryCurrentChat == nil {
		p.t.Fatal("text entry did not use native inline mode")
	}
	return kb, *entry.SwitchInlineQueryCurrentChat
}

func (p *professionsTest) submit(kb *telego.InlineKeyboardMarkup, value string) (*telego.InlineKeyboardMarkup, string) {
	p.t.Helper()
	article := p.article(1, value)
	if strings.Contains(article.InputMessageContent.(*telego.InputTextMessageContent).MessageText, "alice") {
		p.t.Fatal("posting the suggestion would disclose a handle")
	}
	return p.press(1, professionsButton(p.t, kb, "Предпросмотр"))
}

func (p *professionsTest) save(kb *telego.InlineKeyboardMarkup, action, value string) {
	p.t.Helper()
	draft, entry := p.start(kb, action)
	preview, answer := p.submit(draft, entry+value+";")
	if !strings.Contains(strings.ToLower(answer), strings.ToLower(strings.Fields(value)[0])) {
		p.t.Fatalf("preview %q did not identify %q", answer, value)
	}
	if _, answer := p.press(1, professionsButton(p.t, preview, "Сохранить")); answer != "Сохранено." {
		p.t.Fatalf("save: %q", answer)
	}
}

func TestProfessionsBrowseAndPersistAssignmentsByUserID(t *testing.T) {
	p, reopen := newProfessionsTest(t)
	kb, answer := p.open(1, "1")
	if !strings.Contains(answer, "пока нет") {
		t.Fatalf("expected empty professions: %q", answer)
	}
	for _, name := range []string{"Аналитик", "Инженер"} {
		p.save(kb, "Добавить профессию", name)
		kb, _ = p.open(1, "1")
	}
	for number, handles := range map[string]string{"1": "@ALICE, bob_42", "2": "alice"} {
		profession, _ := p.press(1, professionsButton(t, kb, number))
		p.save(profession, "Назначить участников", handles)
	}

	reopen()
	p.api.ChatMemberUsers["100101:7"] = telego.User{Username: "new_alice"}
	p.api.ChatMemberUsers["100101:8"] = telego.User{FirstName: "Борис & Co"}
	kb, answer = p.open(2, "1")
	if !strings.Contains(answer, "Аналитик") || !strings.Contains(answer, "Инженер") {
		t.Fatalf("persistent directory missing: %q", answer)
	}
	for _, number := range []string{"1", "2"} {
		_, answer := p.press(2, professionsButton(t, kb, number))
		if !strings.Contains(answer, "new_alice") || strings.Contains(answer, "previous_alice") || strings.Contains(answer, "Борис & Co") != (number == "1") {
			t.Fatalf("profession %s has wrong participants: %q", number, answer)
		}
	}
	if _, answer := p.open(1, "2"); !strings.Contains(answer, "пока нет") {
		t.Fatalf("first chat leaked into second directory: %q", answer)
	}
}

func TestProfessionsCallbacksRecheckAuthorizationAndOriginalMessage(t *testing.T) {
	p, reopen := newProfessionsTest(t)
	member, _ := p.open(2, "1")
	for _, row := range member.InlineKeyboard {
		for _, button := range row {
			if button.Text == "Добавить профессию" {
				t.Fatal("non-admin received a write button")
			}
		}
	}
	admin, _ := p.open(1, "1")
	add := professionsButton(t, admin, "Добавить профессию")
	if kb, _ := p.press(2, add); kb != nil {
		t.Fatal("a different user could operate the menu")
	}
	draft, entry := p.start(admin, "Добавить профессию")
	preview, _ := p.submit(draft, entry+"Аналитик;")
	save := professionsButton(t, preview, "Сохранить")
	p.board = "different-inline-message"
	if kb, _ := p.press(1, save); kb != nil {
		t.Fatal("confirmation worked on the wrong inline message")
	}
	p.board = "original-inline-message"
	p.api.ChatMembers["100101:1"] = telego.MemberStatusMember
	if kb, _ := p.press(1, save); kb != nil {
		t.Fatal("confirmation worked after admin rights were revoked")
	}
	p.api.ChatMembers["100101:1"] = telego.MemberStatusAdministrator
	reopen()
	if kb, _ := p.press(1, save); kb != nil {
		t.Fatal("confirmation worked after restart")
	}
	if _, answer := p.open(1, "1"); !strings.Contains(answer, "пока нет") {
		t.Fatalf("unauthorized change persisted: %q", answer)
	}
	p.api.ChatMembers["100101:2"] = telego.MemberStatusLeft
	readButton := professionsButton(t, member, "Список профессий")
	if kb, answer := p.press(2, readButton); kb != nil || strings.Contains(answer, "пока нет") {
		t.Fatalf("former member accessed directory: %q, %+v", answer, kb)
	}
}

func TestProfessionsRejectUnknownAndFreezeExactPreview(t *testing.T) {
	p, _ := newProfessionsTest(t)
	kb, _ := p.open(1, "1")
	draft, entry := p.start(kb, "Добавить профессию")
	p.article(1, entry+"Инженер;")
	if preview, answer := p.submit(draft, entry+"Дизайнер;"); preview != nil || !strings.Contains(answer, "заново") {
		t.Fatalf("conflicting final input was accepted: %+v, %q", preview, answer)
	}
	p.press(1, professionsButton(t, draft, "Отмена"))
	kb, _ = p.open(1, "1")
	draft, entry = p.start(kb, "Добавить профессию")
	preview, answer := p.submit(draft, entry+"Аналитик;")
	if !strings.Contains(answer, "Аналитик") {
		t.Fatalf("expected exact preview: %q", answer)
	}
	p.article(1, entry+"Инженер;")
	p.press(1, professionsButton(t, preview, "Сохранить"))
	kb, answer = p.open(1, "1")
	if !strings.Contains(answer, "Аналитик") || strings.Contains(answer, "Инженер") {
		t.Fatalf("late inline update changed preview: %q", answer)
	}
	profession, _ := p.press(1, professionsButton(t, kb, "1"))
	draft, entry = p.start(profession, "Назначить участников")
	if preview, answer := p.submit(draft, entry+"alice, alicia;"); preview != nil || !strings.Contains(answer, "alicia") || !strings.Contains(answer, "напишет") {
		t.Fatalf("unknown handle lacked actionable rejection: %+v, %q", preview, answer)
	}
	p.press(1, professionsButton(t, draft, "Отмена"))
	if _, answer := p.press(1, professionsButton(t, kb, "1")); !strings.Contains(answer, "пока нет") {
		t.Fatalf("unknown handle caused a partial assignment: %q", answer)
	}
}

func TestProfessionsRejectDepartedTargetsAndExpiredDrafts(t *testing.T) {
	p, _ := newProfessionsTest(t)
	kb, _ := p.open(1, "1")
	p.save(kb, "Добавить профессию", "Аналитик")
	kb, _ = p.open(1, "1")
	profession, _ := p.press(1, professionsButton(t, kb, "1"))
	draft, entry := p.start(profession, "Назначить участников")
	preview, answer := p.submit(draft, entry+"alice, bob_42;")
	if !strings.Contains(answer, "alice") || !strings.Contains(answer, "bob_42") {
		t.Fatalf("expected both targets in preview: %q", answer)
	}
	if entries, err := p.menu.directory(100101); err != nil || len(entries) != 1 || len(entries[0].UserIDs) != 0 {
		t.Fatalf("inline entry or preview wrote data: %+v, %v", entries, err)
	}
	save := professionsButton(t, preview, "Сохранить")
	p.api.ChatMembers["100101:8"] = telego.MemberStatusLeft
	if kb, answer := p.press(1, save); kb != nil || !strings.Contains(answer, "bob_42") {
		t.Fatalf("confirmation accepted after departure: %+v, %q", kb, answer)
	}
	p.api.ChatMembers["100101:8"] = telego.MemberStatusMember
	for _, pending := range p.menu.drafts {
		pending.until = time.Now().Add(-time.Second)
	}
	if kb, answer := p.press(1, save); kb != nil || !strings.Contains(answer, "устарела") {
		t.Fatalf("expired confirmation accepted: %+v, %q", kb, answer)
	}
	if _, answer := p.press(1, professionsButton(t, kb, "1")); !strings.Contains(answer, "пока нет") {
		t.Fatalf("rejected confirmation caused a partial assignment: %q", answer)
	}
}
