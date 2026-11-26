package bot

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	"github.com/mymmrac/telego/telegoapi"
	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/shared"
)

const (
	professionsPrefix = "pro:"
	professionsText   = "Реестр профессий"
	professionsStale  = "Меню устарело. Откройте новое через professions."
	professionsTTL    = 30 * time.Minute
	professionsPage   = 8

	professionDirectory = 0
	professionRoles     = 1
	professionStack     = 2
)

type professionsMenu struct {
	api     shared.TelegramAPI
	members membership.Store
	db      *bolt.DB
	botID   int64

	mu       sync.Mutex
	sessions map[string]*professionSession
	tokens   [4096]string
	next     int
}

type professionSession struct {
	mu       sync.Mutex
	actor    int64
	chat     int64
	post     int
	until    time.Time
	mode     int
	focus    string
	revision int
	view     string
	before   []string
	choices  []string
	selected map[string]bool
	group    int
	offset   int
	filter   string
	detailID int64
	visible  [3]int64
}

func newProfessionsMenu(api shared.TelegramAPI, members membership.Store, db *bolt.DB) *professionsMenu {
	return &professionsMenu{api: api, members: members, db: db, sessions: make(map[string]*professionSession)}
}

func (m *professionsMenu) results(_ context.Context, query telego.InlineQuery) ([]telego.InlineQueryResult, bool) {
	fields := strings.Fields(query.Query)
	if len(fields) != 0 && !strings.HasPrefix("professions", strings.ToLower(fields[0])) {
		return nil, false
	}
	if query.From.ID <= 0 || query.From.IsBot || query.ChatType != "" && query.ChatType != telego.ChatTypeSupergroup {
		return nil, true
	}
	if len(fields) > 1 && !strings.EqualFold(fields[0], "professions") {
		return nil, false
	}
	if len(fields) < 2 {
		return []telego.InlineQueryResult{
			m.offer(query.From.ID, professionDirectory, "", professionsText, "Все участники и поиск по профессиям в этом чате."),
		}, true
	}
	cmd := strings.ToLower(fields[1])
	prefix := strings.ToLower(strings.Join(fields[2:], " "))
	var results []telego.InlineQueryResult
	for _, option := range []struct {
		command, title string
		mode           int
	}{
		{"set", "Выбрать профессии", professionRoles},
		{"set-stack", "Выбрать технологии", professionStack},
	} {
		if cmd != option.command && (len(fields) > 2 || !strings.HasPrefix(option.command, cmd)) {
			continue
		}
		if prefix != "" {
			for _, name := range professionSuggestions(option.mode, prefix) {
				results = append(results, m.offer(query.From.ID, option.mode, name, name, "Открыть этот пункт. Отметки будут видны в чате."))
			}
		}
		hint := "Отметьте пункты кнопками. Отметки будут видны в чате."
		if prefix != "" && len(results) == 0 {
			hint = "Совпадений нет. Открыть справочник без добавления."
		}
		results = append(results, m.offer(query.From.ID, option.mode, "", option.title, hint))
		if cmd == option.command {
			break
		}
	}
	return results, true
}

func (m *professionsMenu) offer(actor int64, mode int, focus, title, description string) *telego.InlineQueryResultArticle {
	token := rand.Text()
	m.mu.Lock()
	delete(m.sessions, m.tokens[m.next])
	m.tokens[m.next] = token
	m.next = (m.next + 1) % len(m.tokens)
	m.sessions[token] = &professionSession{actor: actor, mode: mode, focus: focus, until: time.Now().Add(professionsTTL)}
	m.mu.Unlock()
	return &telego.InlineQueryResultArticle{
		Type: telego.ResultTypeArticle, ID: token, Title: title, Description: description,
		InputMessageContent: &telego.InputTextMessageContent{MessageText: professionsText + ". Нажмите «Открыть»."},
		ReplyMarkup: &telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{{
			professionButton(token, 0, "Открыть", "open", 0),
		}}},
	}
}

// observe binds only a posted result from this bot, not the chatless inline query.
func (m *professionsMenu) observe(msg *telego.Message, botID int64) {
	if msg == nil || msg.Chat.Type != telego.ChatTypeSupergroup || msg.Chat.ID >= -1_000_000_000_000 ||
		msg.MessageID <= 0 || msg.From == nil || msg.From.IsBot || msg.SenderChat != nil ||
		msg.ForwardOrigin != nil || msg.ViaBot == nil || msg.ViaBot.ID != botID || msg.ReplyMarkup == nil {
		return
	}
	rows := msg.ReplyMarkup.InlineKeyboard
	if len(rows) != 1 || len(rows[0]) != 1 {
		return
	}
	token, rev, action, arg, ok := professionAction(rows[0][0].CallbackData)
	if !ok || rev != 0 || action != "open" || arg != 0 {
		return
	}
	s := m.session(token)
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.actor == msg.From.ID && s.chat == 0 && time.Now().Before(s.until) {
		s.chat, s.post = msg.Chat.ID, msg.MessageID
	}
}

func (m *professionsMenu) session(token string) *professionSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[token]
}

// TDLib encodes legacy and 64-bit InputBotInlineMessageID bodies as
// base64url. For supergroups, owner is the negated MTProto channel ID.
func professionPostMatches(id string, chat int64, post int) bool {
	if len(id) != 27 && len(id) != 32 {
		return false
	}
	var buf [24]byte
	n, err := base64.RawURLEncoding.Decode(buf[:], []byte(id))
	if err != nil {
		return false
	}
	var owner int64
	var message int32
	switch n {
	case 20:
		owner = int64(int32(binary.LittleEndian.Uint32(buf[8:])))
		message = int32(binary.LittleEndian.Uint32(buf[4:]))
	case 24:
		owner = int64(binary.LittleEndian.Uint64(buf[4:]))
		message = int32(binary.LittleEndian.Uint32(buf[12:]))
	default:
		return false
	}
	return owner < 0 && owner == chat+1_000_000_000_000 && post == int(message)
}

func professionAction(data string) (token string, rev int, action string, arg int, ok bool) {
	data, ok = strings.CutPrefix(data, professionsPrefix)
	if !ok || len(data) > 64-len(professionsPrefix) {
		return "", 0, "", 0, false
	}
	token, rest, _ := strings.Cut(data, ":")
	revision, rest, _ := strings.Cut(rest, ":")
	action, value, _ := strings.Cut(rest, ":")
	rev, err := strconv.Atoi(revision)
	arg, argErr := strconv.Atoi(value)
	return token, rev, action, arg, token != "" && rev >= 0 && arg >= 0 && err == nil && argErr == nil
}

func professionButton(token string, rev int, text, action string, arg int) telego.InlineKeyboardButton {
	return telego.InlineKeyboardButton{Text: text, CallbackData: professionsPrefix + token + ":" + strconv.Itoa(rev) + ":" + action + ":" + strconv.Itoa(arg)}
}

func (m *professionsMenu) callback(ctx context.Context, q telego.CallbackQuery) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	token, rev, action, arg, ok := professionAction(q.Data)
	if !ok || q.InlineMessageID == "" {
		return m.answer(ctx, q, professionsStale)
	}
	s := m.session(token)
	if s == nil || s.actor != q.From.ID {
		return m.answer(ctx, q, "Откройте своё меню через professions.")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !time.Now().Before(s.until) {
		return m.answer(ctx, q, professionsStale)
	}
	if s.chat == 0 {
		return m.answer(ctx, q, "Меню ещё не привязано к группе. Попробуйте снова или откройте его в супергруппе с ботом.")
	}
	if !professionPostMatches(q.InlineMessageID, s.chat, s.post) {
		return m.answer(ctx, q, professionsStale)
	}
	if m.presentMember(ctx, s.chat, s.actor) == nil {
		return m.answer(ctx, q, "Telegram не подтвердил ваше участие в этой группе.")
	}
	if rev != s.revision {
		// Repaint the current state if a rapid tap used a previous keyboard.
		return m.render(ctx, q, token, s, "Меню обновилось. Повторите нажатие.")
	}
	switch action {
	case "open":
		if s.view != "" {
			return m.answer(ctx, q, professionsStale)
		}
		if s.mode == professionDirectory {
			s.view = "home"
		} else if err := m.startChoice(s, s.mode, s.focus); err != nil {
			return m.fail(ctx, q, err)
		}
	case "home":
		s.view = "home"
		s.selected = nil
	case "roles", "stack":
		if s.view != "home" {
			return m.answer(ctx, q, professionsStale)
		}
		mode := professionRoles
		if action == "stack" {
			mode = professionStack
		}
		if err := m.startChoice(s, mode, ""); err != nil {
			return m.fail(ctx, q, err)
		}
	case "mark", "unmark":
		if s.selected == nil || !s.choose(action, arg) {
			return m.answer(ctx, q, professionsStale)
		}
		// Catalog indices are stable across marks; the selected-only list is not.
		if s.view == "selected" {
			s.revision++
		}
		return m.render(ctx, q, token, s, "")
	case "categories", "group", "pick", "pickpage", "selected", "save", "cancel":
		if s.selected == nil {
			return m.answer(ctx, q, professionsStale)
		}
		if action == "save" {
			if err := m.saveChoice(s); err != nil {
				if errors.Is(err, errProfessionConflict) {
					return m.answer(ctx, q, "Профиль уже изменился. Откройте выбор заново, чтобы не затереть изменения.")
				}
				return m.fail(ctx, q, err)
			}
			s.view, s.selected = "home", nil
			s.revision++
			return m.render(ctx, q, token, s, "Сохранено.")
		}
		if !s.choose(action, arg) {
			return m.answer(ctx, q, professionsStale)
		}
	case "detail":
		if s.view != "people" || arg >= len(s.visible) || s.visible[arg] == 0 {
			return m.answer(ctx, q, professionsStale)
		}
		s.detailID, s.view, s.offset = s.visible[arg], "detail", 0
	case "detailPage":
		if s.view != "detail" {
			return m.answer(ctx, q, professionsStale)
		}
		s.offset = arg
	case "backPeople":
		if s.view != "detail" {
			return m.answer(ctx, q, professionsStale)
		}
		s.view, s.offset = "people", 0
	case "all", "by", "byPage", "filter", "peoplePage":
		changed, err := m.navigate(s, action, arg)
		if err != nil {
			return m.fail(ctx, q, err)
		}
		if !changed {
			return m.answer(ctx, q, professionsStale)
		}
	default:
		return m.answer(ctx, q, professionsStale)
	}
	s.revision++
	return m.render(ctx, q, token, s, "")
}

func (m *professionsMenu) presentMember(ctx context.Context, chat, user int64) *telego.User {
	member, err := m.api.GetChatMember(ctx, &telego.GetChatMemberParams{ChatID: telego.ChatID{ID: chat}, UserID: user})
	if err != nil || member == nil || !member.MemberIsMember() {
		return nil
	}
	acc := member.MemberUser()
	if acc.ID != user || acc.IsBot {
		return nil
	}
	return &acc
}

func (m *professionsMenu) render(ctx context.Context, q telego.CallbackQuery, token string, s *professionSession, feedback string) error {
	button := func(text, action string, arg int) telego.InlineKeyboardButton {
		return professionButton(token, s.revision, text, action, arg)
	}
	var rows [][]telego.InlineKeyboardButton
	text := professionsText
	switch s.view {
	case "home":
		rows = [][]telego.InlineKeyboardButton{
			{button("Все участники", "all", 0), button("По профессиям", "by", 0)},
			{button("Мои профессии", "roles", 0), button("Мой стек", "stack", 0)},
		}
	case "pick", "categories", "selected":
		text, rows = s.picker(button)
	case "detail":
		var err error
		rows, feedback, err = m.detail(ctx, s, button)
		if err != nil {
			return m.fail(ctx, q, err)
		}
	case "by", "people":
		var err error
		text, rows, feedback, err = m.browse(ctx, s, button)
		if err != nil {
			return m.fail(ctx, q, err)
		}
	default:
		return m.answer(ctx, q, professionsStale)
	}
	if err := m.answer(ctx, q, feedback); err != nil {
		return err
	}
	_, err := m.api.EditMessageText(ctx, &telego.EditMessageTextParams{
		InlineMessageID: q.InlineMessageID, Text: text,
		ReplyMarkup: &telego.InlineKeyboardMarkup{InlineKeyboard: rows},
	})
	var apiErr *telegoapi.Error
	if errors.As(err, &apiErr) && strings.Contains(apiErr.Description, "message is not modified") {
		return nil
	}
	return err
}

func (m *professionsMenu) fail(ctx context.Context, q telego.CallbackQuery, err error) error {
	return errors.Join(err, m.answer(ctx, q, "Не удалось выполнить действие. Попробуйте ещё раз."))
}

func (m *professionsMenu) answer(ctx context.Context, q telego.CallbackQuery, text string) error {
	return m.api.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{
		CallbackQueryID: q.ID, Text: text, ShowAlert: text != "",
	})
}

func professionPageButtons(button func(string, string, int) telego.InlineKeyboardButton, action string, offset, size, total int) []telego.InlineKeyboardButton {
	var row []telego.InlineKeyboardButton
	if offset > 0 {
		row = append(row, button("‹ Назад", action, max(0, offset-size)))
	}
	if offset < total-size {
		row = append(row, button("Далее ›", action, offset+size))
	}
	return row
}

func professionGrid(buttons []telego.InlineKeyboardButton) [][]telego.InlineKeyboardButton {
	rows := make([][]telego.InlineKeyboardButton, 0, (len(buttons)+1)/2)
	for len(buttons) > 0 {
		n := min(2, len(buttons))
		rows, buttons = append(rows, buttons[:n:n]), buttons[n:]
	}
	return rows
}

func professionSelectionLabel(count int) string {
	return fmt.Sprintf("Выбрано: %d", count)
}
