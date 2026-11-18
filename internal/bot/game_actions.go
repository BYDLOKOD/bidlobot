package bot

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/gameengine"
	"github.com/veschin/bidlobot/internal/gameengine/prod"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
)

type gameActions struct {
	engine    *gameengine.Engine
	api       shared.TelegramAPI
	log       *slog.Logger
	key       []byte
	farmLink  func(int64) string
	farmParse func(string) (string, string, error)
	display   shared.DisplayResolver
}

// AttachGameEngine wires the shared engine before Run. The signing key stays
// server-side and binds each button to its chat and, when necessary, its actor.
func (a *App) AttachGameEngine(engine *gameengine.Engine, key string, farmLink func(int64) string, farmParse func(string) (string, string, error), display shared.DisplayResolver) {
	a.gameActions = &gameActions{engine: engine, api: a.sender, log: a.log, key: []byte(key), farmLink: farmLink, farmParse: farmParse, display: display}
}

func (g *gameActions) member(ctx context.Context, chatID, userID int64) bool {
	member, err := g.api.GetChatMember(ctx, &telego.GetChatMemberParams{ChatID: telego.ChatID{ID: chatID}, UserID: userID})
	return err == nil && member != nil && member.MemberIsMember()
}

func (g *gameActions) command(module string) th.MessageHandler {
	return func(_ *th.Context, msg telego.Message) error {
		if msg.From == nil || msg.From.IsBot || msg.SenderChat != nil {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if !g.member(ctx, msg.Chat.ID, msg.From.ID) {
			return nil
		}
		verb, arg := "view", ""
		if module == "prod" {
			verb = "show"
		}
		if module == "farm" {
			var err error
			verb, arg, err = g.farmParse(commandArgs(msg.Text))
			if err != nil {
				return g.send(ctx, msg.Chat.ID, msg.MessageThreadID, msg.From.ID, module, gameengine.Result{Text: err.Error()})
			}
		}
		r, err := g.engine.Apply(module, gameengine.Action{ChatID: storage.AbsChatID(msg.Chat.ID), UserID: msg.From.ID, Key: fmt.Sprintf("m%d", msg.MessageID), Verb: verb, Arg: arg, Now: time.Now().UTC()})
		if err != nil {
			r = gameengine.Result{Text: g.errorText(err)}
		}
		return g.send(ctx, msg.Chat.ID, msg.MessageThreadID, msg.From.ID, module, r)
	}
}

func gameButtonOwner(owner int64, module, verb string) int64 {
	if module == "prod" {
		switch verb {
		case "join", "start", "cancel", "vote", "clue", "retro", "status":
			return 0
		}
	}
	return owner
}

func (g *gameActions) sign(chatID, actor int64, data string) string {
	mac := hmac.New(sha256.New, g.key)
	_, _ = fmt.Fprintf(mac, "%d:%d:%s", chatID, actor, data)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:8])
}

func (g *gameActions) keyboard(chatID, userID int64, module string, result gameengine.Result) *telego.InlineKeyboardMarkup {
	rows := make([][]telego.InlineKeyboardButton, 0, len(result.Buttons)+1)
	for _, b := range result.Buttons {
		body := "play|" + module + "|" + b.Verb + "|" + b.Arg
		payload := body + "|" + g.sign(chatID, gameButtonOwner(userID, module, b.Verb), body)
		if len(payload) > 64 {
			g.log.Error("game button exceeds Telegram limit", "module", module, "verb", b.Verb)
			continue
		}
		rows = append(rows, []telego.InlineKeyboardButton{{Text: b.Label, CallbackData: payload}})
	}
	if module == "farm" && g.farmLink != nil {
		if link := g.farmLink(chatID); link != "" {
			rows = append(rows, []telego.InlineKeyboardButton{{Text: "Открыть карту фермы", URL: link}})
		}
	}
	return &telego.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func (g *gameActions) send(ctx context.Context, chatID int64, topic int, actor int64, module string, result gameengine.Result) error {
	if result.Text == "" {
		return nil
	}
	if module == "prod" {
		result.Text = g.incidentNames(ctx, chatID, result.Text)
	}
	markup := g.keyboard(chatID, actor, module, result)
	params := &telego.SendMessageParams{ChatID: telego.ChatID{ID: chatID}, MessageThreadID: topic, Text: result.Text}
	if len(markup.InlineKeyboard) > 0 {
		params.ReplyMarkup = markup
	}
	_, err := g.api.SendMessage(ctx, params)
	return err
}

func (g *gameActions) errorText(err error) string {
	var rejection gameengine.Rejection
	if errors.As(err, &rejection) {
		return rejection.Error()
	}
	g.log.Error("game action", "error", err)
	return "Игра не сохранила действие. Попробуйте открыть карточку заново."
}

func (g *gameActions) callback(_ *th.Context, q telego.CallbackQuery) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	answer := func(s string) error {
		return g.api.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: s, ShowAlert: s != ""})
	}
	p := strings.Split(q.Data, "|")
	msg, ok := q.Message.(*telego.Message)
	if len(p) != 5 || !ok || msg.Chat.Type != telego.ChatTypeSupergroup || q.From.IsBot {
		return answer("Откройте игру в группе.")
	}
	module, verb, arg := p[1], p[2], p[3]
	owner := gameButtonOwner(q.From.ID, module, verb)
	body := q.Data[:len(q.Data)-len(p[4])-1]
	if !hmac.Equal([]byte(g.sign(msg.Chat.ID, owner, body)), []byte(p[4])) {
		return answer("Эта кнопка недоступна. Откройте свою карточку.")
	}
	if !g.member(ctx, msg.Chat.ID, q.From.ID) {
		return answer("Это действие доступно участникам чата.")
	}
	key := "c" + q.ID
	// A paid confirmation belongs to the original card, not to each tap.
	if (module == "automat" && verb == "confirm") || (module == "prod" && verb == "open") || (module == "farm" && (verb == "buy" || verb == "accept")) {
		key = fmt.Sprintf("p%d:%s", msg.MessageID, body)
	}
	a := gameengine.Action{ChatID: storage.AbsChatID(msg.Chat.ID), UserID: q.From.ID, CardID: msg.MessageID, Key: key, Verb: verb, Arg: arg, Now: time.Now().UTC()}
	if module == "prod" && verb == prod.VerbStart {
		var candidates []int64
		err := g.engine.View(func(tx *bolt.Tx) error {
			var err error
			candidates, err = prod.LobbyParticipants(tx, a.ChatID, a.UserID, arg)
			return err
		})
		if err != nil {
			return answer(g.errorText(err))
		}
		a.Eligible = make([]int64, 0, len(candidates))
		for _, id := range candidates {
			if id == a.UserID || g.member(ctx, msg.Chat.ID, id) {
				a.Eligible = append(a.Eligible, id)
			}
		}
		a.Now = time.Now().UTC()
	}
	r, err := g.engine.Apply(module, a)
	if err != nil {
		return answer(g.errorText(err))
	}
	if err := answer(""); err != nil {
		g.log.Debug("game callback acknowledgement", "error", err)
	}
	if r.Text == "" {
		return nil
	}
	if module == "prod" && verb == prod.VerbRetro && !strings.HasPrefix(msg.Text, "Разбор инцидента ") {
		return g.send(ctx, msg.Chat.ID, msg.MessageThreadID, q.From.ID, module, r)
	}
	if module == "prod" && verb != prod.VerbRetro {
		return nil // The worker updates the shared card from the committed render intent.
	}
	if module == "prod" {
		r.Text = g.incidentNames(ctx, msg.Chat.ID, r.Text)
	}
	_, err = g.api.EditMessageText(ctx, &telego.EditMessageTextParams{ChatID: telego.ChatID{ID: msg.Chat.ID}, MessageID: msg.MessageID, Text: r.Text, ReplyMarkup: g.keyboard(msg.Chat.ID, q.From.ID, module, r)})
	return err
}

func (g *gameActions) tick(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if _, err := g.engine.Advance(now.UTC()); err != nil {
				g.log.Error("game deadline", "error", err)
			}
			if err := g.renderIncidents(ctx); err != nil {
				g.log.Error("read incident render queue", "error", err)
			}
		}
	}
}

func (g *gameActions) renderIncidents(ctx context.Context) error {
	queue, err := g.engine.PendingRenders()
	if err != nil {
		return err
	}
	for _, entry := range queue {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		chatID := -entry.ChatID
		r := entry.Result
		r.Text = g.incidentNames(ctx, chatID, r.Text)
		sendCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, editErr := g.api.EditMessageText(sendCtx, &telego.EditMessageTextParams{ChatID: telego.ChatID{ID: chatID}, MessageID: entry.CardID, Text: r.Text, ReplyMarkup: g.keyboard(chatID, 0, "prod", r)})
		cancel()
		if editErr != nil && !strings.Contains(editErr.Error(), "message is not modified") && !strings.Contains(editErr.Error(), "message to edit not found") {
			g.log.Warn("render incident", "error", editErr)
			continue
		}
		if err := g.engine.AcknowledgeRender(entry); err != nil {
			return err
		}
	}
	return nil
}

func gameCallbackPredicate() th.Predicate {
	return func(_ context.Context, u telego.Update) bool {
		return u.CallbackQuery != nil && strings.HasPrefix(u.CallbackQuery.Data, "play|")
	}
}

func commandArgs(text string) string {
	if i := strings.IndexFunc(strings.TrimSpace(text), unicode.IsSpace); i >= 0 {
		return strings.TrimSpace(strings.TrimSpace(text)[i:])
	}
	return ""
}

var incidentActor = regexp.MustCompile(`Участник ([0-9]+)`)

func (g *gameActions) incidentNames(ctx context.Context, chatID int64, text string) string {
	if g.display == nil {
		return text
	}
	return incidentActor.ReplaceAllStringFunc(text, func(raw string) string {
		id, _ := strconv.ParseInt(strings.TrimPrefix(raw, "Участник "), 10, 64)
		name := g.display.UserDisplay(ctx, storage.AbsChatID(chatID), id)
		if name == "" {
			return raw
		}
		name = strings.Join(strings.Fields(name), " ")
		return strings.ReplaceAll(name, "@", "@\u2060")
	})
}
