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
	"github.com/veschin/bidlobot/internal/gameengine"
	"github.com/veschin/bidlobot/internal/gameengine/automat"
	"github.com/veschin/bidlobot/internal/storage"
	"github.com/veschin/bidlobot/internal/testutil"
)

type excuseTestAPI struct {
	*testutil.MockAPI
	user  telego.User
	posts []telego.SendMessageParams
}

func (m *excuseTestAPI) GetChatMember(_ context.Context, p *telego.GetChatMemberParams) (telego.ChatMember, error) {
	if p.UserID != m.user.ID {
		return nil, errors.New("unknown member")
	}
	return &telego.ChatMemberMember{Status: "member", User: m.user}, nil
}

func (m *excuseTestAPI) SendMessageOnce(_ context.Context, p *telego.SendMessageParams) (*telego.Message, error) {
	m.posts = append(m.posts, *p)
	return nil, context.DeadlineExceeded
}

func TestExcuseRepliesToCurrentMentionOnce(t *testing.T) {
	db, err := storage.NewBoltStore(filepath.Join(t.TempDir(), "mention.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := storage.NewMembershipRepo(db.DB())
	api := &excuseTestAPI{MockAPI: testutil.NewMockAPI(), user: telego.User{ID: 42, Username: "lena"}}
	if _, err := repo.UpsertMember(context.Background(), membership.MemberPatch{UserID: 42, AbsChatID: 100, Username: &api.user.Username}); err != nil {
		t.Fatal(err)
	}
	engine := gameengine.New(db.DB(), map[string]gameengine.Module{"automat": automat.Apply})
	app := &App{sender: api, memberSvc: membership.NewService(repo), log: testLogger()}
	app.AttachGameEngine(engine, "fixture", nil, nil, nil)
	apply := func(verb, arg string) gameengine.Result {
		t.Helper()
		r, err := engine.Apply("automat", gameengine.Action{ChatID: 100, UserID: 42, Key: verb, Verb: verb, Arg: arg, Now: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	buttonArg := func(r gameengine.Result, verb string) string {
		t.Helper()
		for _, b := range r.Buttons {
			if b.Verb == verb {
				return b.Arg
			}
		}
		t.Fatalf("no %s button on %q", verb, r.Text)
		return ""
	}
	prompt := apply("spin", "")
	prize := apply("confirm", buttonArg(prompt, "confirm"))
	apply("equip", buttonArg(prize, "equip"))
	apply("enable", "")
	msg := &telego.Message{Chat: telego.Chat{ID: -100, Type: telego.ChatTypeSupergroup}, MessageID: 819, MessageThreadID: 7, From: &telego.User{ID: 12}, Text: "🌾 @lena"}
	app.replyWithExcuse(msg) // Literal text is not an entity.
	msg.Entities = []telego.MessageEntity{{Type: "mention", Offset: 3, Length: 5}}
	api.user.Username = "other"
	app.replyWithExcuse(msg) // The cached handle no longer belongs to this member.
	if len(api.posts) != 0 {
		t.Fatalf("unexpected reply: %+v", api.posts)
	}
	api.user.Username = "lena"
	app.replyWithExcuse(msg)
	app.replyWithExcuse(msg) // An ambiguous Telegram send does not repeat.
	if len(api.posts) != 1 {
		t.Fatalf("got %d sends, want one", len(api.posts))
	}
	p := api.posts[0]
	if p.ReplyParameters == nil || p.ReplyParameters.MessageID != msg.MessageID || p.MessageThreadID != 7 || p.ParseMode != "" || strings.Contains(p.Text, "@lena") || !strings.HasPrefix(p.Text, "Отмазка для lena:\n") {
		t.Fatalf("wrong reply attribution or destination: %+v", p)
	}
}
