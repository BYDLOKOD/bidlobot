package bot

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/gameengine/automat"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
)

func (a *App) excuseMentionMiddleware(h *th.Context, update telego.Update) error {
	if msg := update.Message; msg != nil && msg.From != nil && !msg.From.IsBot && msg.SenderChat == nil && !strings.HasPrefix(strings.TrimSpace(msg.Text), "/") && (len(msg.Entities) > 0 || len(msg.CaptionEntities) > 0) {
		a.replyWithExcuse(msg)
	}
	return h.Next(update)
}

func (a *App) replyWithExcuse(msg *telego.Message) {
	g := a.gameActions
	sender, ok := g.api.(interface {
		SendMessageOnce(context.Context, *telego.SendMessageParams) (*telego.Message, error)
	})
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	text, entities := msg.Text, msg.Entities
	if text == "" {
		text, entities = msg.Caption, msg.CaptionEntities
	}
	short := make([]telego.MessageEntity, 0, len(entities))
	for _, e := range entities {
		if e.Type == "mention" || e.Type == "text_mention" {
			short = append(short, e)
		}
	}
	if len(short) == 0 {
		return
	}
	slices.SortStableFunc(short, func(a, b telego.MessageEntity) int {
		if a.Offset < b.Offset {
			return -1
		}
		if a.Offset > b.Offset {
			return 1
		}
		return 0
	})
	chatID := storage.AbsChatID(msg.Chat.ID)
	units := utf16.Encode([]rune(text))
	for _, e := range short {
		var id int64
		var handle string
		switch {
		case e.Type == "text_mention" && e.User != nil && !e.User.IsBot:
			id = e.User.ID
		case e.Type == "mention" && a.memberSvc != nil && e.Offset >= 0 && e.Length > 1 && e.Offset <= len(units) && e.Length <= len(units)-e.Offset:
			var ok bool
			handle, ok = strings.CutPrefix(string(utf16.Decode(units[e.Offset:e.Offset+e.Length])), "@")
			if !ok || handle == "" {
				continue
			}
			member, err := a.memberSvc.Store().GetMemberByUsername(ctx, chatID, handle)
			if err != nil || member == nil {
				continue
			}
			id = member.UserID
		}
		if id <= 0 || id == msg.From.ID {
			continue
		}
		var optedIn bool
		if err := g.engine.View(func(tx *bolt.Tx) error {
			settings, err := automat.Lookup(tx, chatID, id)
			optedIn = settings.Enabled && settings.Equipped.ID != 0
			return err
		}); err != nil || !optedIn {
			continue
		}
		live, err := g.api.GetChatMember(ctx, &telego.GetChatMemberParams{ChatID: telego.ChatID{ID: msg.Chat.ID}, UserID: id})
		if err != nil || live == nil || !live.MemberIsMember() {
			continue
		}
		user := live.MemberUser()
		if user.ID != id || user.IsBot || (handle != "" && !strings.EqualFold(user.Username, handle)) {
			continue
		}
		var pending *automat.PendingReply
		if err := g.engine.Update(func(tx *bolt.Tx) error {
			var err error
			pending, err = automat.ReserveReply(tx, chatID, id, int64(msg.MessageID), time.Now().UTC())
			return err
		}); err != nil {
			g.log.Warn("reserve excuse reply", "error", err)
			return
		}
		if pending == nil {
			continue
		}
		var phrase string
		var allowed bool
		if err := g.engine.View(func(tx *bolt.Tx) error {
			var err error
			phrase, allowed, err = automat.DeliveryPhrase(tx, chatID, id)
			return err
		}); err != nil || !allowed || phrase != pending.Phrase {
			return
		}
		name := shared.UserDisplay(user.Username, user.FirstName)
		if name == "" {
			name = "участник"
		}
		name = strings.ReplaceAll(name, "@", "@\u2060")
		_, err = sender.SendMessageOnce(ctx, &telego.SendMessageParams{ChatID: telego.ChatID{ID: msg.Chat.ID}, MessageThreadID: msg.MessageThreadID, Text: fmt.Sprintf("Отмазка (%s): \u00ab%s\u00bb", name, phrase), ReplyParameters: &telego.ReplyParameters{MessageID: msg.MessageID}})
		if err != nil {
			g.log.Warn("deliver excuse reply", "error", err)
		}
		return
	}
}
