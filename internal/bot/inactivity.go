package bot

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/mymmrac/telego"
	"github.com/mymmrac/telego/telegoapi"
	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/storage"
)

const (
	inactivityPrefix       = "ina:"
	inactivityDefaultHours = 48
	inactivityMaxHours     = 365 * 24
	inactivityAlertLimit   = 200
	inactivityTokenTTL     = 2 * time.Hour
)

var inactivitySettingsBucket = []byte("inactivity_settings")

type inactivitySession struct {
	actor, chat int64
	expires     time.Time
}

type inactivityMenu struct {
	api     shared.TelegramAPI
	members membership.Store
	db      *bolt.DB

	mu       sync.Mutex
	sessions map[string]inactivitySession
	tokens   [2048]string
	next     int
}

func newInactivityMenu(api shared.TelegramAPI, members membership.Store, db *bolt.DB) *inactivityMenu {
	return &inactivityMenu{api: api, members: members, db: db, sessions: make(map[string]inactivitySession)}
}

func (m *inactivityMenu) start(actor, chat int64) string {
	token := rand.Text()
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, m.tokens[m.next])
	m.tokens[m.next] = token
	m.next = (m.next + 1) % len(m.tokens)
	m.sessions[token] = inactivitySession{actor: actor, chat: chat, expires: time.Now().Add(inactivityTokenTTL)}
	return token
}

func (m *inactivityMenu) session(token string, actor int64) (inactivitySession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[token]
	return s, ok && s.actor == actor && time.Now().Before(s.expires)
}

func (m *inactivityMenu) results(ctx context.Context, query telego.InlineQuery) ([]telego.InlineQueryResult, bool) {
	text := strings.TrimSpace(query.Query)
	if text == "" {
		return []telego.InlineQueryResult{inactivityGuide("Введите inactive и, при необходимости, название супергруппы.")}, true
	}
	cmd, hint := text, ""
	if i := strings.IndexFunc(text, unicode.IsSpace); i >= 0 {
		cmd, hint = text[:i], text[i:]
	}
	if !strings.EqualFold(cmd, "inactive") {
		return nil, false
	}
	chats, err := m.members.ListChats(ctx)
	if err != nil {
		return []telego.InlineQueryResult{inactivityGuide("Не удалось загрузить список супергрупп. Попробуйте снова.")}, true
	}
	hint = strings.ToLower(strings.TrimSpace(hint))
	searchCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var results []telego.InlineQueryResult
	for _, chat := range currentBotChats(chats) {
		if searchCtx.Err() != nil || len(results) == 49 {
			return append(results, inactivityGuide("Уточните название супергруппы после inactive.")), true
		}
		title := ownerChatName(chat)
		if !strings.Contains(strings.ToLower(title), hint) || !m.admin(searchCtx, chat.AbsChatID, query.From.ID) {
			continue
		}
		token := m.start(query.From.ID, chat.AbsChatID)
		results = append(results, &telego.InlineQueryResultArticle{
			Type: telego.ResultTypeArticle, ID: token, Title: title, Description: "Открыть меню неактивных участников",
			InputMessageContent: &telego.InputTextMessageContent{MessageText: "Неактивные участники. Список доступен только вам."},
			ReplyMarkup: &telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{
				{inactivityButton(token, "Открыть", "m")},
			}},
		})
	}
	if len(results) == 0 {
		results = append(results, inactivityGuide("Нет доступных супергрупп. Для поиска введите inactive и название."))
	}
	return results, true
}

func inactivityGuide(text string) telego.InlineQueryResult {
	query := "inactive "
	return &telego.InlineQueryResultArticle{
		Type: telego.ResultTypeArticle, ID: "inactivity-guide", Title: "Неактивные участники", Description: text,
		InputMessageContent: &telego.InputTextMessageContent{MessageText: text},
		ReplyMarkup: &telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{
			{{Text: "Выбрать супергруппу", SwitchInlineQueryCurrentChat: &query}},
		}},
	}
}

func (m *inactivityMenu) admin(ctx context.Context, chat, actor int64) bool {
	if chat <= 0 || actor <= 0 {
		return false
	}
	member, err := m.api.GetChatMember(ctx, &telego.GetChatMemberParams{ChatID: telego.ChatID{ID: -chat}, UserID: actor})
	if err != nil || member == nil {
		return false
	}
	status := member.MemberStatus()
	return status == telego.MemberStatusCreator || status == telego.MemberStatusAdministrator
}

func (m *inactivityMenu) callback(ctx context.Context, query telego.CallbackQuery) error {
	answer := func(text string) error {
		return m.api.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{
			CallbackQueryID: query.ID, Text: text, ShowAlert: text != "",
		})
	}
	data, prefixed := strings.CutPrefix(query.Data, inactivityPrefix)
	token, action, found := strings.Cut(data, ":")
	if !prefixed || !found || query.InlineMessageID == "" || query.Message != nil {
		return answer("Откройте меню в режиме inline.")
	}
	session, ok := m.session(token, query.From.ID)
	if !ok {
		return answer("Это меню вам недоступно. Откройте своё через inactive.")
	}
	chat, err := m.members.GetChat(ctx, session.chat)
	if err != nil && !errors.Is(err, membership.ErrChatNotFound) {
		return errors.Join(err, answer("Не удалось проверить доступ."))
	}
	if chat == nil || chat.Type != telego.ChatTypeSupergroup || !botStatusIsCurrent(chat.BotStatus) {
		return answer("Супергруппа недоступна.")
	}
	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	admin := m.admin(checkCtx, session.chat, query.From.ID)
	cancel()
	if !admin {
		return answer("Меню доступно только администраторам супергруппы.")
	}
	hours, err := m.interval(session.chat)
	if err != nil {
		return errors.Join(err, answer("Не удалось загрузить порог неактивности."))
	}
	button := func(label, action string) telego.InlineKeyboardButton {
		return inactivityButton(token, label, action)
	}
	op, arg, _ := strings.Cut(action, ":")
	var alert, text string
	var rows [][]telego.InlineKeyboardButton
	switch op {
	case "m":
		text = fmt.Sprintf("Неактивные участники. Порог: %d ч.\nСписок откроется только вам.", hours)
		rows = [][]telego.InlineKeyboardButton{{button("Список", "p:0"), button("Порог", "s")}}
	case "s", "v":
		if op == "v" {
			value, err := strconv.Atoi(arg)
			if err != nil || value < 1 || value > inactivityMaxHours {
				return answer("Недопустимый порог неактивности.")
			}
			if value != hours {
				if err := m.setInterval(session.chat, value); err != nil {
					return errors.Join(err, answer("Не удалось сохранить порог неактивности."))
				}
				hours = value
			}
		}
		text = fmt.Sprintf("Порог неактивности: %d ч.", hours)
		var changes []telego.InlineKeyboardButton
		for _, change := range []int{-24, -1, 1, 24} {
			if value := hours + change; value >= 1 && value <= inactivityMaxHours {
				changes = append(changes, button(fmt.Sprintf("%+d ч", change), "v:"+strconv.Itoa(value)))
			}
		}
		rows = [][]telego.InlineKeyboardButton{changes, {button("Сбросить", "v:48"), button("К меню", "m")}}
	case "p":
		page, err := strconv.Atoi(arg)
		if err != nil || page < 0 {
			return answer("Страница недоступна.")
		}
		now := time.Now()
		members, err := m.inactiveMembers(ctx, session.chat, hours, now)
		if err != nil {
			return errors.Join(err, answer("Не удалось загрузить список участников."))
		}
		var more bool
		alert, page, more = inactivityPage(members, now, page)
		text = fmt.Sprintf("Неактивные участники. Порог: %d ч. Страница: %d.", hours, page+1)
		var nav []telego.InlineKeyboardButton
		if page > 0 {
			nav = append(nav, button("Назад", "p:"+strconv.Itoa(page-1)))
		}
		nav = append(nav, button("Повторить", "p:"+strconv.Itoa(page)))
		if more {
			nav = append(nav, button("Далее", "p:"+strconv.Itoa(page+1)))
		}
		rows = [][]telego.InlineKeyboardButton{nav, {button("К меню", "m")}}
	default:
		return answer("Действие недоступно.")
	}
	if err := answer(alert); err != nil {
		return err
	}
	_, err = m.api.EditMessageText(ctx, &telego.EditMessageTextParams{
		InlineMessageID: query.InlineMessageID, Text: text,
		ReplyMarkup: &telego.InlineKeyboardMarkup{InlineKeyboard: rows},
	})
	var apiErr *telegoapi.Error
	if errors.As(err, &apiErr) && strings.HasPrefix(apiErr.Description, "Bad Request: message is not modified") {
		return nil
	}
	return err
}

func (m *inactivityMenu) interval(chat int64) (int, error) {
	hours := inactivityDefaultHours
	err := m.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(inactivitySettingsBucket)
		if b == nil {
			return bolt.ErrBucketNotFound
		}
		if value := b.Get(storage.ChatKey(chat)); value != nil {
			parsed, err := strconv.Atoi(string(value))
			if err != nil || parsed < 1 || parsed > inactivityMaxHours {
				return errors.New("invalid inactivity interval")
			}
			hours = parsed
		}
		return nil
	})
	return hours, err
}

func (m *inactivityMenu) setInterval(chat int64, hours int) error {
	return m.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(inactivitySettingsBucket)
		if b == nil {
			return bolt.ErrBucketNotFound
		}
		return b.Put(storage.ChatKey(chat), strconv.AppendInt(nil, int64(hours), 10))
	})
}

func (m *inactivityMenu) inactiveMembers(ctx context.Context, chat int64, hours int, now time.Time) ([]membership.Member, error) {
	members, err := m.members.ListByChat(ctx, chat)
	if err != nil {
		return nil, err
	}
	cutoff := now.Add(-time.Duration(hours) * time.Hour)
	filtered := members[:0]
	for _, member := range members {
		if member.IsBot || member.Status == membership.StatusLeft || member.Status == membership.StatusKicked ||
			member.LastMessageAt.IsZero() || member.MessageCount <= 0 || !member.LastMessageAt.Before(cutoff) {
			continue
		}
		filtered = append(filtered, member)
	}
	return filtered, nil
}

func inactivityPage(members []membership.Member, now time.Time, requested int) (text string, page int, more bool) {
	if len(members) == 0 {
		return "Нет неактивных участников с записанными сообщениями.", 0, false
	}
	var batch strings.Builder
	units := 0
	for _, member := range members {
		line := inactivityLine(member, now)
		size := inactivityTextUnits(line)
		if units != 0 && units+1+size > inactivityAlertLimit {
			if page == requested {
				return batch.String(), page, true
			}
			page++
			batch.Reset()
			units = 0
		}
		if units != 0 {
			batch.WriteByte('\n')
			units++
		}
		batch.WriteString(line)
		units += size
	}
	return batch.String(), page, false
}

func inactivityLine(member membership.Member, now time.Time) string {
	name := member.Username
	if name == "" && member.KnownVia != membership.SourceImport {
		name = member.FirstName
	}
	name = strings.Join(strings.FieldsFunc(name, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}), " ")
	if name == "" {
		name = fmt.Sprintf("Участник %d", member.UserID)
	}
	flag := func(days int) string {
		if now.Sub(member.LastMessageAt) >= time.Duration(days)*24*time.Hour {
			return "да"
		}
		return "нет"
	}
	suffix := fmt.Sprintf(": %d сообщ. · 7д: %s · 30д: %s", member.MessageCount, flag(7), flag(30))
	limit := inactivityAlertLimit - inactivityTextUnits(suffix)
	if inactivityTextUnits(name) > limit {
		const ellipsis = "..."
		limit -= len(ellipsis)
		units := 0
		for i, r := range name {
			units += inactivityRuneUnits(r)
			if units > limit {
				name = name[:i] + ellipsis
				break
			}
		}
	}
	return name + suffix
}

func inactivityTextUnits(text string) int {
	units := 0
	for _, r := range text {
		units += inactivityRuneUnits(r)
	}
	return units
}

func inactivityRuneUnits(r rune) int {
	if r > 0xffff {
		return 2
	}
	return 1
}

func inactivityButton(token, label, action string) telego.InlineKeyboardButton {
	return telego.InlineKeyboardButton{Text: label, CallbackData: inactivityPrefix + token + ":" + action}
}
