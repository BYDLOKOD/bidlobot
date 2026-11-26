package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	professionsText    = "Реестр профессий. Названия и имена видны только вам в подсказках."
	professionsStale   = "Кнопка устарела. Откройте меню или начните ввод заново."
	professionsDenied  = "Telegram не подтвердил ваше право на это действие в выбранном чате."
	professionsPage    = 4
	professionsDrafts  = 512
	professionsNameMax = 36
)

var (
	professionsBucket = []byte("professions")
	professionExists  = errors.New("profession already exists")
)

type professionsMenu struct {
	api     shared.TelegramAPI
	members membership.Store
	db      *bolt.DB
	mu      sync.Mutex
	drafts  map[string]*professionDraft
}

type profession struct {
	Name    string  `json:"name"`
	UserIDs []int64 `json:"user_ids,omitempty"`
}

type professionTarget struct {
	id     int64
	handle string
}

type professionDraft struct {
	actor, chat, index int64
	message            string
	until              time.Time
	input              string
	conflict           bool
	sealed             bool
	ready              bool
	targets            []professionTarget
}

func newProfessionsMenu(api shared.TelegramAPI, members membership.Store, db *bolt.DB) *professionsMenu {
	return &professionsMenu{api: api, members: members, db: db, drafts: make(map[string]*professionDraft)}
}

func (m *professionsMenu) results(_ context.Context, query telego.InlineQuery) ([]telego.InlineQueryResult, bool) {
	cmd, rest, _ := strings.Cut(strings.TrimSpace(query.Query), " ")
	if cmd != "" && !strings.EqualFold(cmd, "professions") {
		return nil, false
	}
	hint := "Откройте меню и выберите чат."
	entry, draftQuery := strings.CutPrefix(strings.TrimSpace(rest), "edit ")
	if draftQuery {
		token, input, _ := strings.Cut(entry, " ")
		hint = m.capture(query.From.ID, token, input)
	}
	return []telego.InlineQueryResult{&telego.InlineQueryResultArticle{
		Type: telego.ResultTypeArticle, ID: "professions", Title: "Реестр профессий", Description: hint,
		InputMessageContent: &telego.InputTextMessageContent{MessageText: professionsText},
		ReplyMarkup: &telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{{
			professionButton("Выбрать чат", query.From.ID, "open", 0),
		}}},
	}}, true
}

func (m *professionsMenu) capture(actor int64, token, input string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	draft := m.drafts[token]
	if draft == nil || draft.actor != actor || !time.Now().Before(draft.until) {
		return "Начните ввод заново с кнопки в меню."
	}
	if draft.sealed {
		return "Вернитесь к меню. Чтобы изменить ввод, нажмите кнопку Отмена."
	}
	value, final := strings.CutSuffix(strings.TrimSpace(input), ";")
	if len(value) > 256 || !final || strings.TrimSpace(value) == "" {
		return "Закончите ввод знаком ;. Не отправляйте результат в чат."
	}
	value = strings.TrimSpace(value)
	if draft.input == "" {
		draft.input = value
	} else if draft.input != value {
		draft.conflict = true
	}
	if draft.conflict {
		return "Получено два варианта. Нажмите кнопку Отмена и начните ввод заново."
	}
	return "Вернитесь к исходному меню и нажмите кнопку Предпросмотр. Результат отправлять не нужно."
}

func (m *professionsMenu) callback(ctx context.Context, q telego.CallbackQuery) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	parts := strings.Split(q.Data, ":")
	if q.InlineMessageID == "" || len(parts) < 4 || parts[0] != "pro" || q.From.ID <= 0 || parts[1] != strconv.FormatInt(q.From.ID, 36) {
		return m.answer(ctx, q, "Откройте своё меню через inline-поиск professions.")
	}
	action := parts[2]
	switch action {
	case "preview", "confirm", "cancel":
		if len(parts) != 4 {
			return m.answer(ctx, q, professionsStale)
		}
		return m.finishDraft(ctx, q, action, parts[3])
	}
	var numbers [3]int64
	if len(parts) > 6 {
		return m.answer(ctx, q, professionsStale)
	}
	for i, part := range parts[3:] {
		var err error
		numbers[i], err = strconv.ParseInt(part, 36, 64)
		if err != nil || numbers[i] < 0 {
			return m.answer(ctx, q, professionsStale)
		}
	}
	chat, index, offset := numbers[0], numbers[1], numbers[2]
	if action == "open" && len(parts) == 4 {
		return m.chooseChat(ctx, q, chat)
	}
	if chat == 0 {
		return m.answer(ctx, q, professionsStale)
	}
	status := m.role(ctx, chat, q.From.ID)
	if status == "" {
		return m.answer(ctx, q, professionsDenied)
	}
	admin := status == telego.MemberStatusCreator || status == telego.MemberStatusAdministrator
	switch {
	case action == "list" && len(parts) == 5:
		m.discardDraft(q)
		return m.browse(ctx, q, chat, -1, index, admin)
	case action == "people" && len(parts) == 6:
		m.discardDraft(q)
		return m.browse(ctx, q, chat, index, offset, admin)
	case action == "add" && len(parts) == 4:
		index = -1
	case action == "assign" && len(parts) == 5:
	default:
		return m.answer(ctx, q, professionsStale)
	}
	if !admin {
		return m.answer(ctx, q, professionsDenied)
	}
	return m.startDraft(ctx, q, chat, index)
}

func (m *professionsMenu) chooseChat(ctx context.Context, q telego.CallbackQuery, offset int64) error {
	m.discardDraft(q)
	chats, err := m.members.ListChats(ctx)
	if err != nil {
		return m.fail(ctx, q, err)
	}
	chats = slices.DeleteFunc(chats, func(chat membership.Chat) bool {
		return chat.Type != telego.ChatTypeSupergroup || !botStatusIsCurrent(chat.BotStatus)
	})
	kb := &telego.InlineKeyboardMarkup{}
	var text strings.Builder
	if offset < int64(len(chats)) {
		for i, chat := range chats[offset:min(offset+professionsPage, int64(len(chats)))] {
			if m.presentMember(ctx, chat.AbsChatID, q.From.ID) == nil {
				continue
			}
			n := strconv.Itoa(i + 1)
			fmt.Fprintf(&text, "%s. %s\n", n, professionLabel(ownerChatName(chat)))
			kb.InlineKeyboard = append(kb.InlineKeyboard, []telego.InlineKeyboardButton{
				professionButton(n, q.From.ID, "list", chat.AbsChatID, 0),
			})
		}
	}
	alert := strings.TrimSpace(text.String())
	if alert == "" {
		alert = "На этой странице нет доступных чатов."
	}
	professionPager(kb, q.From.ID, "open", offset, int64(len(chats)))
	kb.InlineKeyboard = append(kb.InlineKeyboard, []telego.InlineKeyboardButton{professionButton("Показать чаты", q.From.ID, "open", offset)})
	return m.show(ctx, q, professionsText, kb, alert)
}

func (m *professionsMenu) browse(ctx context.Context, q telego.CallbackQuery, chat, index, offset int64, admin bool) error {
	entries, err := m.directory(chat)
	if err != nil {
		return m.fail(ctx, q, err)
	}
	if index >= int64(len(entries)) {
		return m.answer(ctx, q, professionsStale)
	}
	kb := &telego.InlineKeyboardMarkup{}
	var text strings.Builder
	actor := q.From.ID
	listOffset := offset
	if index < 0 {
		if len(entries) == 0 {
			text.WriteString("В этом чате пока нет профессий.")
		}
		if offset < int64(len(entries)) {
			for i, entry := range entries[offset:min(offset+professionsPage, int64(len(entries)))] {
				n := strconv.Itoa(i + 1)
				fmt.Fprintf(&text, "%s. %s\n", n, entry.Name)
				kb.InlineKeyboard = append(kb.InlineKeyboard, []telego.InlineKeyboardButton{
					professionButton(n, actor, "people", chat, offset+int64(i), 0),
				})
			}
		}
		professionPager(kb, actor, "list", offset, int64(len(entries)), chat)
		if admin {
			kb.InlineKeyboard = append(kb.InlineKeyboard, []telego.InlineKeyboardButton{professionButton("Добавить профессию", actor, "add", chat)})
		}
	} else {
		listOffset = index / professionsPage * professionsPage
		entry := entries[index]
		text.WriteString(entry.Name)
		if len(entry.UserIDs) == 0 {
			text.WriteString("\nУ профессии пока нет участников.")
		}
		if offset < int64(len(entry.UserIDs)) {
			visible := false
			for _, id := range entry.UserIDs[offset:min(offset+professionsPage, int64(len(entry.UserIDs)))] {
				member := m.presentMember(ctx, chat, id)
				if member != nil {
					user := member.MemberUser()
					label := user.Username
					if label == "" {
						label = user.FirstName
					}
					if label == "" {
						label = strconv.FormatInt(id, 10)
					}
					fmt.Fprintf(&text, "\n- %s", professionLabel(label))
					visible = true
				}
			}
			if !visible {
				text.WriteString("\nНа этой странице нет доступных участников.")
			}
		}
		professionPager(kb, actor, "people", offset, int64(len(entry.UserIDs)), chat, index)
		if admin {
			kb.InlineKeyboard = append(kb.InlineKeyboard, []telego.InlineKeyboardButton{professionButton("Назначить участников", actor, "assign", chat, index)})
		}
		kb.InlineKeyboard = append(kb.InlineKeyboard, []telego.InlineKeyboardButton{professionButton("Показать участников", actor, "people", chat, index, offset)})
	}
	kb.InlineKeyboard = append(kb.InlineKeyboard, []telego.InlineKeyboardButton{
		professionButton("Список профессий", actor, "list", chat, listOffset),
		professionButton("Выбрать чат", actor, "open", 0),
	})
	if text.Len() == 0 {
		return m.answer(ctx, q, professionsStale)
	}
	return m.show(ctx, q, professionsText, kb, strings.TrimSpace(text.String()))
}

func (m *professionsMenu) startDraft(ctx context.Context, q telego.CallbackQuery, chat, index int64) error {
	token, err := storage.NewID()
	if err != nil {
		return m.fail(ctx, q, err)
	}
	now := time.Now()
	m.mu.Lock()
	for key, draft := range m.drafts {
		if !now.Before(draft.until) || draft.actor == q.From.ID && draft.message == q.InlineMessageID {
			delete(m.drafts, key)
		}
	}
	if len(m.drafts) >= professionsDrafts {
		m.mu.Unlock()
		return m.answer(ctx, q, "Слишком много черновиков. Попробуйте через несколько минут.")
	}
	m.drafts[token] = &professionDraft{
		actor: q.From.ID, chat: chat, index: index, message: q.InlineMessageID, until: now.Add(10 * time.Minute),
	}
	m.mu.Unlock()
	entry := "professions edit " + token + " "
	hint := "После имени бота введите " + entry + "название профессии;"
	if index >= 0 {
		hint = "После имени бота введите " + entry + "ники через запятую (до четырёх);"
	}
	kb := &telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{
		{{Text: "Ввести текст", SwitchInlineQueryCurrentChat: &entry}},
		{professionDraftButton("Предпросмотр", q.From.ID, "preview", token), professionDraftButton("Отмена", q.From.ID, "cancel", token)},
	}}
	if err := m.answer(ctx, q, hint); err != nil {
		return err
	}
	text := "Откройте ввод. Когда подсказка примет текст, вернитесь к этому меню и нажмите кнопку Предпросмотр."
	err = m.edit(ctx, q, text, kb)
	var apiErr *telegoapi.Error
	if errors.As(err, &apiErr) && strings.Contains(apiErr.Description, "BUTTON_TYPE_INVALID") {
		kb.InlineKeyboard = kb.InlineKeyboard[1:]
		return m.edit(ctx, q, "Введите запрос по подсказке и вернитесь сюда для предпросмотра.", kb)
	}
	return err
}

func (m *professionsMenu) discardDraft(q telego.CallbackQuery) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for token, draft := range m.drafts {
		if draft.actor == q.From.ID && draft.message == q.InlineMessageID {
			delete(m.drafts, token)
			return
		}
	}
}

func (m *professionsMenu) finishDraft(ctx context.Context, q telego.CallbackQuery, action, token string) error {
	m.mu.Lock()
	draft := m.drafts[token]
	if draft == nil || draft.actor != q.From.ID || draft.message != q.InlineMessageID || !time.Now().Before(draft.until) {
		m.mu.Unlock()
		return m.answer(ctx, q, professionsStale)
	}
	if action == "cancel" {
		delete(m.drafts, token)
	}
	m.mu.Unlock()

	status := m.role(ctx, draft.chat, q.From.ID)
	if status != telego.MemberStatusCreator && status != telego.MemberStatusAdministrator {
		return m.answer(ctx, q, professionsDenied)
	}
	if action == "cancel" {
		return m.browse(ctx, q, draft.chat, draft.index, 0, true)
	}
	m.mu.Lock()
	if m.drafts[token] != draft || !time.Now().Before(draft.until) || action == "preview" && draft.sealed || action == "confirm" && !draft.ready {
		m.mu.Unlock()
		return m.answer(ctx, q, professionsStale)
	}
	if action == "preview" {
		if draft.input == "" || draft.conflict {
			alert := "Закончите ввод знаком ; и вернитесь к предпросмотру."
			if draft.conflict {
				alert = "Получено два варианта. Нажмите кнопку Отмена и начните ввод заново."
			}
			m.mu.Unlock()
			return m.answer(ctx, q, alert)
		}
		draft.sealed = true
	}
	input, targets := draft.input, draft.targets
	m.mu.Unlock()
	if action == "preview" {
		return m.preview(ctx, q, token, draft, input)
	}
	for _, target := range targets {
		if !m.matchesHandle(ctx, draft.chat, target) {
			return m.answer(ctx, q, "Участник "+target.handle+" уже сменил ник или вышел из чата. Начните ввод заново.")
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	m.mu.Lock()
	if m.drafts[token] != draft || !time.Now().Before(draft.until) {
		m.mu.Unlock()
		return m.answer(ctx, q, professionsStale)
	}
	delete(m.drafts, token)
	m.mu.Unlock()
	if err := m.save(draft.chat, draft.index, input, targets); err != nil {
		if errors.Is(err, professionExists) {
			return m.answer(ctx, q, "Такая профессия уже есть. Выберите её в списке.")
		}
		return m.fail(ctx, q, err)
	}
	kb := &telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{{
		professionButton("Список профессий", q.From.ID, "list", draft.chat, max(draft.index, 0)/professionsPage*professionsPage),
		professionButton("Выбрать чат", q.From.ID, "open", 0),
	}}}
	return m.show(ctx, q, professionsText, kb, "Сохранено.")
}

func (m *professionsMenu) preview(ctx context.Context, q telego.CallbackQuery, token string, draft *professionDraft, input string) error {
	alert := input
	var targets []professionTarget
	if draft.index < 0 {
		if !validProfessionName(input) {
			return m.answer(ctx, q, "Введите название длиной до 36 знаков. Нажмите кнопку Отмена и начните ввод заново.")
		}
	} else {
		entries, err := m.directory(draft.chat)
		if err != nil {
			return m.fail(ctx, q, err)
		}
		if draft.index >= int64(len(entries)) {
			return m.answer(ctx, q, professionsStale)
		}
		handles := professionHandles(input)
		if handles == nil {
			return m.answer(ctx, q, "Введите до четырёх ников через пробел или запятую. Нажмите кнопку Отмена и начните ввод заново.")
		}
		var unknown string
		targets, unknown, err = m.resolveTargets(ctx, draft.chat, handles)
		if err != nil {
			return m.fail(ctx, q, err)
		}
		if unknown != "" {
			return m.answer(ctx, q, "Участник "+unknown+" не найден. Проверьте ник. Если бот его не видел, пусть участник напишет в чат. Начните ввод заново.")
		}
		alert = entries[draft.index].Name + "\n@" + strings.Join(handles, ", @")
	}
	if err := m.answer(ctx, q, alert); err != nil {
		return err
	}
	m.mu.Lock()
	if m.drafts[token] != draft || !time.Now().Before(draft.until) {
		m.mu.Unlock()
		return nil
	}
	draft.targets, draft.ready = targets, true
	m.mu.Unlock()
	kb := &telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{{
		professionDraftButton("Сохранить", q.From.ID, "confirm", token), professionDraftButton("Отмена", q.From.ID, "cancel", token),
	}}}
	return m.edit(ctx, q, "Подтвердите вариант, показанный в подсказке, или отмените ввод.", kb)
}

func (m *professionsMenu) role(ctx context.Context, chat, actor int64) string {
	record, err := m.members.GetChat(ctx, chat)
	if err != nil || record.Type != telego.ChatTypeSupergroup || !botStatusIsCurrent(record.BotStatus) {
		return ""
	}
	if member := m.presentMember(ctx, chat, actor); member != nil {
		return member.MemberStatus()
	}
	return ""
}

func (m *professionsMenu) presentMember(ctx context.Context, chat, user int64) telego.ChatMember {
	member, err := m.api.GetChatMember(ctx, &telego.GetChatMemberParams{ChatID: telego.ChatID{ID: -chat}, UserID: user})
	if err != nil || member == nil || !member.MemberIsMember() || member.MemberUser().ID != user {
		return nil
	}
	return member
}

func (m *professionsMenu) resolveTargets(ctx context.Context, chat int64, handles []string) ([]professionTarget, string, error) {
	members, err := m.members.ListByChat(ctx, chat)
	if err != nil {
		return nil, "", err
	}
	targets := make([]professionTarget, 0, len(handles))
	for _, handle := range handles {
		var match professionTarget
		for _, member := range members {
			if !strings.EqualFold(member.Username, handle) || member.UserID <= 0 {
				continue
			}
			target := professionTarget{id: member.UserID, handle: handle}
			if m.matchesHandle(ctx, chat, target) {
				if match.id != 0 {
					return nil, handle, nil
				}
				match = target
			}
		}
		if match.id == 0 {
			return nil, handle, nil
		}
		targets = append(targets, match)
	}
	return targets, "", nil
}

func (m *professionsMenu) matchesHandle(ctx context.Context, chat int64, target professionTarget) bool {
	member := m.presentMember(ctx, chat, target.id)
	if member == nil {
		return false
	}
	user := member.MemberUser()
	return !user.IsBot && strings.EqualFold(user.Username, target.handle)
}

func (m *professionsMenu) directory(chat int64) ([]profession, error) {
	var entries []profession
	err := m.db.View(func(tx *bolt.Tx) error {
		return readProfessions(tx.Bucket(professionsBucket), storage.ChatKey(chat), &entries)
	})
	return entries, err
}

func readProfessions(bucket *bolt.Bucket, key []byte, entries *[]profession) error {
	if bucket == nil {
		return bolt.ErrBucketNotFound
	}
	if data := bucket.Get(key); data != nil {
		return json.Unmarshal(data, entries)
	}
	return nil
}

func (m *professionsMenu) save(chat, index int64, input string, targets []professionTarget) error {
	return m.db.Update(func(tx *bolt.Tx) error {
		bucket, key := tx.Bucket(professionsBucket), storage.ChatKey(chat)
		var entries []profession
		if err := readProfessions(bucket, key, &entries); err != nil {
			return err
		}
		if index < 0 {
			for _, entry := range entries {
				if strings.EqualFold(entry.Name, input) {
					return professionExists
				}
			}
			entries = append(entries, profession{Name: input})
		} else {
			if index >= int64(len(entries)) {
				return errors.New("profession not found")
			}
			entry := &entries[index]
			for _, target := range targets {
				if !slices.Contains(entry.UserIDs, target.id) {
					entry.UserIDs = append(entry.UserIDs, target.id)
				}
			}
		}
		data, err := json.Marshal(entries)
		if err != nil {
			return err
		}
		return bucket.Put(key, data)
	})
}

func validProfessionName(input string) bool {
	n := 0
	for _, r := range input {
		if !unicode.IsPrint(r) || r == ';' {
			return false
		}
		n++
		if r > 0xffff {
			n++
		}
		if n > professionsNameMax {
			return false
		}
	}
	return n > 0
}

func professionHandles(input string) []string {
	var handles []string
	for _, field := range strings.FieldsFunc(input, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		handle := strings.TrimPrefix(field, "@")
		if len(handle) == 0 || len(handle) > 32 {
			return nil
		}
		for _, r := range handle {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
				return nil
			}
		}
		handle = strings.ToLower(handle)
		if !slices.Contains(handles, handle) {
			if len(handles) == professionsPage {
				return nil
			}
			handles = append(handles, handle)
		}
	}
	return handles
}

func professionLabel(label string) string {
	label = strings.Join(strings.Fields(label), " ")
	n := 0
	for i, r := range label {
		if n >= professionsNameMax-4 {
			return label[:i] + "..."
		}
		n++
		if r > 0xffff {
			n++
		}
	}
	return label
}

func professionButton(text string, actor int64, action string, numbers ...int64) telego.InlineKeyboardButton {
	data := "pro:" + strconv.FormatInt(actor, 36) + ":" + action
	for _, n := range numbers {
		data += ":" + strconv.FormatInt(n, 36)
	}
	return telego.InlineKeyboardButton{Text: text, CallbackData: data}
}

func professionDraftButton(text string, actor int64, action, token string) telego.InlineKeyboardButton {
	button := professionButton(text, actor, action)
	button.CallbackData += ":" + token
	return button
}

func professionPager(kb *telego.InlineKeyboardMarkup, actor int64, action string, offset, total int64, prefix ...int64) {
	var buttons []telego.InlineKeyboardButton
	if offset > 0 {
		buttons = append(buttons, professionButton("Предыдущая страница", actor, action, append(prefix, max(0, offset-professionsPage))...))
	}
	if offset < total-professionsPage {
		buttons = append(buttons, professionButton("Следующая страница", actor, action, append(prefix, offset+professionsPage)...))
	}
	if len(buttons) > 0 {
		kb.InlineKeyboard = append(kb.InlineKeyboard, buttons)
	}
}

func (m *professionsMenu) show(ctx context.Context, q telego.CallbackQuery, text string, kb *telego.InlineKeyboardMarkup, alert string) error {
	if err := m.answer(ctx, q, alert); err != nil {
		return err
	}
	return m.edit(ctx, q, text, kb)
}

func (m *professionsMenu) fail(ctx context.Context, q telego.CallbackQuery, err error) error {
	return errors.Join(err, m.answer(ctx, q, "Действие не выполнено. Вернитесь в меню и попробуйте ещё раз."))
}

func (m *professionsMenu) edit(ctx context.Context, q telego.CallbackQuery, text string, kb *telego.InlineKeyboardMarkup) error {
	_, err := m.api.EditMessageText(ctx, &telego.EditMessageTextParams{InlineMessageID: q.InlineMessageID, Text: text, ReplyMarkup: kb})
	var apiErr *telegoapi.Error
	if errors.As(err, &apiErr) && strings.Contains(apiErr.Description, "message is not modified") {
		return nil
	}
	return err
}

func (m *professionsMenu) answer(ctx context.Context, q telego.CallbackQuery, text string) error {
	err := m.api.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: text, ShowAlert: true})
	if err == nil {
		return nil
	}
	var apiErr *telegoapi.Error
	if errors.As(err, &apiErr) {
		return fmt.Errorf("answer professions callback: Telegram error %d", apiErr.ErrorCode)
	}
	return errors.New("answer professions callback failed")
}
