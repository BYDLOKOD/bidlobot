package bot

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mymmrac/telego"
	bolt "go.etcd.io/bbolt"

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
		{AbsChatID: -professionChatA, Type: telego.ChatTypeSupergroup, BotStatus: membership.StatusAdministrator},
		{AbsChatID: -professionChatB, Type: telego.ChatTypeSupergroup, BotStatus: membership.StatusAdministrator},
	} {
		if err := repo.UpsertChat(ctx, chat); err != nil {
			t.Fatal(err)
		}
	}
	for userID, username := range map[int64]string{1: "owner", 7: "alice", 8: "bob_42"} {
		if _, err := repo.UpsertMember(ctx, membership.MemberPatch{AbsChatID: -professionChatA, UserID: userID, Username: &username}); err != nil {
			t.Fatal(err)
		}
	}
	api.ChatMemberUsers["1000000100101:1"] = telego.User{Username: "owner"}
	api.ChatMemberUsers["1000000100101:7"] = telego.User{Username: "alice"}
	api.ChatMemberUsers["1000000100101:8"] = telego.User{Username: "bob_42"}
	api.ChatMemberUsers["1000000100102:1"] = telego.User{Username: "owner"}
	p := &professionsTest{t: t, menu: newProfessionsMenu(api, repo, store.DB()), api: api}
	return p, func() {
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		store = open()
		p.menu = newProfessionsMenu(api, storage.NewMembershipRepo(store.DB()), store.DB())
	}
}

const (
	professionChatA int64 = -1000000100101
	professionChatB int64 = -1000000100102
)

func professionTestID(chat int64, post int) string {
	var raw [24]byte
	binary.LittleEndian.PutUint32(raw[:], 2)
	binary.LittleEndian.PutUint64(raw[4:], uint64(chat+1_000_000_000_000))
	binary.LittleEndian.PutUint32(raw[12:], uint32(post))
	return base64.RawURLEncoding.EncodeToString(raw[:])
}

func (p *professionsTest) article(actor int64, query, title string) *telego.InlineQueryResultArticle {
	p.t.Helper()
	results, handled := p.menu.results(context.Background(), telego.InlineQuery{From: telego.User{ID: actor}, Query: query})
	for _, result := range results {
		article := result.(*telego.InlineQueryResultArticle)
		if handled && article.Title == title {
			return article
		}
	}
	p.t.Fatalf("query %q: no result %q, got %+v", query, title, results)
	return nil
}

func (p *professionsTest) observe(actor, chat int64, post int, article *telego.InlineQueryResultArticle) {
	p.menu.observe(&telego.Message{
		Chat: telego.Chat{ID: chat, Type: telego.ChatTypeSupergroup}, MessageID: post,
		From: &telego.User{ID: actor}, ViaBot: &telego.User{ID: 123, IsBot: true},
		ReplyMarkup: article.ReplyMarkup,
	}, 123)
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
			if strings.Contains(args.Text, "@alice") || strings.Contains(args.Text, "@owner") {
				p.t.Fatalf("private data appeared in shared edit: %q", args.Text)
			}
			for _, row := range args.ReplyMarkup.InlineKeyboard {
				for _, button := range row {
					if strings.Contains(button.Text, "@alice") || strings.Contains(button.Text, "@owner") {
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

func (p *professionsTest) open(chat int64, post int, query, title string) *telego.InlineKeyboardMarkup {
	p.t.Helper()
	result := p.article(1, query, title)
	p.observe(1, chat, post, result)
	p.board = professionTestID(chat, post)
	kb, _ := p.press(1, professionsButton(p.t, result.ReplyMarkup, "Открыть"))
	return kb
}

func (p *professionsTest) click(kb *telego.InlineKeyboardMarkup, label string) *telego.InlineKeyboardMarkup {
	p.t.Helper()
	kb, _ = p.press(1, professionsButton(p.t, kb, label))
	return kb
}

func TestProfessionsStackMultiSelectAndChatIsolation(t *testing.T) {
	p, reopen := newProfessionsTest(t)
	a := p.article(1, "professions set-stack", "Выбрать технологии")
	b := p.article(1, "professions set-stack clo", "Clojure")
	p.observe(1, professionChatB, 5, b)
	p.observe(1, professionChatA, 5, a)
	p.board = professionTestID(professionChatB, 5)
	if kb, _ := p.press(1, professionsButton(t, a.ReplyMarkup, "Открыть")); kb != nil {
		t.Fatal("a different group opened the first menu")
	}
	p.board = professionTestID(professionChatA, 5)
	kb := p.click(a.ReplyMarkup, "Открыть")
	kb = p.click(kb, "Python")
	kb = p.click(kb, "Docker")
	kb = p.click(kb, "Разделы")
	kb = p.click(kb, "Инструменты и архитектура")
	kb = p.click(kb, "C4")
	kb = p.click(kb, "oh-my-pi")
	kb = p.click(kb, "pi")
	kb = p.click(kb, "Выбрано: 5")
	kb = p.click(kb, "✓ Docker")
	if kb, _ := p.press(7, professionsButton(t, kb, "Сохранить")); kb != nil {
		t.Fatal("another member could save the author profile")
	}
	if _, answer := p.press(1, professionsButton(t, kb, "Сохранить")); answer != "Сохранено." {
		t.Fatalf("save: %q", answer)
	}
	p.board = professionTestID(professionChatB, 5)
	kb = p.click(b.ReplyMarkup, "Открыть")
	kb = p.click(kb, "Clojure")
	p.click(kb, "Сохранить")
	reopen()
	kb = p.open(professionChatA, 6, "professions set-stack", "Выбрать технологии")
	professionsButton(t, kb, "✓ Python")
	professionsButton(t, kb, "Docker")
	kb = p.click(kb, "Выбрано: 4")
	for _, name := range []string{"✓ C4", "✓ oh-my-pi", "✓ pi"} {
		professionsButton(t, kb, name)
	}
	kb = p.open(professionChatB, 6, "professions set-stack", "Выбрать технологии")
	kb = p.click(kb, "Выбрано: 1")
	professionsButton(t, kb, "✓ Clojure")
}

func TestProfessionsKeepsRapidSelectionsFromOneKeyboard(t *testing.T) {
	p, _ := newProfessionsTest(t)
	kb := p.open(professionChatA, 9, "professions set-stack", "Выбрать технологии")
	python := professionsButton(t, kb, "Python")
	docker := professionsButton(t, kb, "Docker")
	p.press(1, python)
	p.press(1, python)
	kb, _ = p.press(1, docker)
	kb = p.click(kb, "Выбрано: 2")
	professionsButton(t, kb, "✓ Python")
	professionsButton(t, kb, "✓ Docker")
}

func TestProfessionsSelfAssignAndBrowsePrivately(t *testing.T) {
	p, _ := newProfessionsTest(t)
	if err := p.menu.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(professionsBucket).Put(storage.ChatKey(-professionChatA), []byte(`[{"name":"Аналитик","user_ids":[1]}]`))
	}); err != nil {
		t.Fatal(err)
	}
	kb := p.open(professionChatA, 5, "professions set arch", "Software Architect")
	kb = p.click(kb, "Software Architect")
	kb = p.click(kb, "Full-Stack Developer")
	p.click(kb, "Сохранить")
	kb = p.open(professionChatA, 6, "professions set-stack invented-tool", "Выбрать технологии")
	kb = p.click(kb, "Python")
	p.click(kb, "Отмена")
	kb = p.open(professionChatA, 7, "professions", "Реестр профессий")
	kb, summary := p.press(1, professionsButton(t, kb, "Все участники"))
	if !strings.Contains(summary, "@alice") || !strings.Contains(summary, "@owner") {
		t.Fatalf("all participants: %q", summary)
	}
	kb = p.click(kb, "По профессиям")
	kb, summary = p.press(1, professionsButton(t, kb, "Software Architect"))
	if !strings.Contains(summary, "@owner") || strings.Contains(summary, "@alice") {
		t.Fatalf("filtered participants: %q", summary)
	}
	kb, profile := p.press(1, professionsButton(t, kb, "Участник 1"))
	for _, want := range []string{"Full-Stack Developer", "Аналитик"} {
		if !strings.Contains(profile, want) {
			t.Fatalf("profile %q does not contain %q", profile, want)
		}
	}
	_, profile = p.press(1, professionsButton(t, kb, "Далее ›"))
	if !strings.Contains(profile, "Стек: не указан") {
		t.Fatalf("second page lost stack state: %q", profile)
	}
}

func TestProfessionsDoesNotOpenUnboundOrReplayedPost(t *testing.T) {
	p, _ := newProfessionsTest(t)
	article := p.article(1, "professions", "Реестр профессий")
	open := professionsButton(t, article.ReplyMarkup, "Открыть")
	p.board = professionTestID(professionChatA, 12)
	if kb, answer := p.press(1, open); kb != nil || !strings.Contains(answer, "не привязано") {
		t.Fatalf("unbound menu: %q", answer)
	}
	p.observe(1, professionChatA, 12, article)
	p.observe(1, professionChatB, 13, article)
	p.board = professionTestID(professionChatB, 13)
	if kb, _ := p.press(1, open); kb != nil {
		t.Fatal("second post rebound menu")
	}
	p.board = professionTestID(professionChatA, 12)
	kb := p.click(article.ReplyMarkup, "Открыть")
	p.api.ChatMembers["1000000100101:1"] = telego.MemberStatusLeft
	if edited, answer := p.press(1, professionsButton(t, kb, "Все участники")); edited != nil || !strings.Contains(answer, "не подтвердил") {
		t.Fatalf("former member accessed the directory: %q", answer)
	}
}
