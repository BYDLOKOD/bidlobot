package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/mymmrac/telego"
	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/storage"
)

var (
	professionsBucket      = []byte("professions")
	professionStacksBucket = []byte("profession_stacks")
	errProfessionConflict  = errors.New("profile changed since opening the menu")
)

type profession struct {
	Name    string  `json:"name"`
	UserIDs []int64 `json:"user_ids,omitempty"`
}

func readProfessionValue(b *bolt.Bucket, key []byte, value any) error {
	if b == nil {
		return bolt.ErrBucketNotFound
	}
	if raw := b.Get(key); raw != nil {
		return json.Unmarshal(raw, value)
	}
	return nil
}

func professionChoices(tx *bolt.Tx, chat, user int64, mode int) ([]string, error) {
	var names []string
	if mode == professionStack {
		err := readProfessionValue(tx.Bucket(professionStacksBucket), storage.MemberChatIndex(-chat, user), &names)
		return names, err
	}
	var entries []profession
	if err := readProfessionValue(tx.Bucket(professionsBucket), storage.ChatKey(-chat), &entries); err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if slices.Contains(entry.UserIDs, user) {
			name := canonicalProfession(entry.Name)
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	slices.Sort(names)
	return names, nil
}

func (m *professionsMenu) currentChoice(chat, user int64, mode int) ([]string, error) {
	var names []string
	err := m.db.View(func(tx *bolt.Tx) error {
		var err error
		names, err = professionChoices(tx, chat, user, mode)
		return err
	})
	return names, err
}

func (m *professionsMenu) saveChoice(s *professionSession) error {
	names := s.picked()
	return m.db.Update(func(tx *bolt.Tx) error {
		current, err := professionChoices(tx, s.chat, s.actor, s.mode)
		if err != nil {
			return err
		}
		if slices.Equal(current, names) {
			return nil
		}
		if !slices.Equal(current, s.before) {
			return errProfessionConflict
		}
		if s.mode == professionStack {
			b, key := tx.Bucket(professionStacksBucket), storage.MemberChatIndex(-s.chat, s.actor)
			if len(names) == 0 {
				return b.Delete(key)
			}
			raw, err := json.Marshal(names)
			if err != nil {
				return err
			}
			return b.Put(key, raw)
		}
		b, key := tx.Bucket(professionsBucket), storage.ChatKey(-s.chat)
		var entries []profession
		if err := readProfessionValue(b, key, &entries); err != nil {
			return err
		}
		seen := make(map[string]bool, len(names))
		for i := range entries {
			entry := &entries[i]
			name := canonicalProfession(entry.Name)
			has := slices.Contains(entry.UserIDs, s.actor)
			if !s.selected[name] {
				if has {
					entry.UserIDs = slices.DeleteFunc(entry.UserIDs, func(id int64) bool { return id == s.actor })
				}
			} else if !seen[name] {
				if !has {
					entry.UserIDs = append(entry.UserIDs, s.actor)
				}
				seen[name] = true
			}
		}
		for _, name := range names {
			if !seen[name] {
				entries = append(entries, profession{Name: name, UserIDs: []int64{s.actor}})
			}
		}
		raw, err := json.Marshal(entries)
		if err != nil {
			return err
		}
		return b.Put(key, raw)
	})
}

func (m *professionsMenu) directory(chat int64) ([]string, map[int64][]string, error) {
	names := slices.Clone(professionNames)
	roles := make(map[int64][]string)
	err := m.db.View(func(tx *bolt.Tx) error {
		var entries []profession
		if err := readProfessionValue(tx.Bucket(professionsBucket), storage.ChatKey(-chat), &entries); err != nil {
			return err
		}
		for _, entry := range entries {
			name := canonicalProfession(entry.Name)
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
			for _, user := range entry.UserIDs {
				if !slices.Contains(roles[user], name) {
					roles[user] = append(roles[user], name)
				}
			}
		}
		return nil
	})
	return names, roles, err
}

func (m *professionsMenu) navigate(s *professionSession, action string, arg int) (bool, error) {
	switch action {
	case "all", "by":
		if s.view != "home" && s.view != "people" && s.view != "by" {
			return false, nil
		}
		s.view, s.offset, s.filter = "by", 0, ""
		if action == "all" {
			s.view = "people"
		}
	case "byPage":
		if s.view != "by" {
			return false, nil
		}
		s.offset = arg
	case "filter":
		if s.view != "by" {
			return false, nil
		}
		names, _, err := m.directory(s.chat)
		if err != nil || arg >= len(names) {
			return false, err
		}
		s.view, s.offset, s.filter = "people", 0, names[arg]
	case "peoplePage":
		if s.view != "people" {
			return false, nil
		}
		s.offset = arg
	}
	return true, nil
}

func (m *professionsMenu) browse(ctx context.Context, s *professionSession, button func(string, string, int) telego.InlineKeyboardButton) (string, [][]telego.InlineKeyboardButton, string, error) {
	names, roles, err := m.directory(s.chat)
	if err != nil {
		return "", nil, "", err
	}
	if s.view == "by" {
		offset := professionBoundPage(s.offset, len(names), professionsPage)
		var buttons []telego.InlineKeyboardButton
		for i := offset; i < min(offset+professionsPage, len(names)); i++ {
			buttons = append(buttons, button(names[i], "filter", i))
		}
		rows := professionGrid(buttons)
		if nav := professionPageButtons(button, "byPage", offset, professionsPage, len(names)); len(nav) != 0 {
			rows = append(rows, nav)
		}
		rows = append(rows, []telego.InlineKeyboardButton{button("К меню", "home", 0)})
		return "По профессиям", rows, "", nil
	}
	known, err := m.members.ListByChat(ctx, -s.chat)
	if err != nil {
		return "", nil, "", err
	}
	ids := make([]int64, 0, len(known))
	for _, member := range known {
		if member.IsBot || member.Status == membership.StatusLeft || member.Status == membership.StatusKicked {
			continue
		}
		if s.filter == "" || slices.Contains(roles[member.UserID], s.filter) {
			ids = append(ids, member.UserID)
		}
	}
	const pageSize = 3
	offset := professionBoundPage(s.offset, len(ids), pageSize)
	title := "Все участники"
	if s.filter != "" {
		title = s.filter
	}
	var rows [][]telego.InlineKeyboardButton
	var summary []string
	clear(s.visible[:])
	for i, id := range ids[offset:min(offset+pageSize, len(ids))] {
		member := m.presentMember(ctx, s.chat, id)
		if member == nil {
			continue
		}
		s.visible[i] = id
		number := strconv.Itoa(i + 1)
		summary = append(summary, professionLimit(number+". "+professionMemberLabel(*member)+professionRoleLabel(roles[id]), 62))
		rows = append(rows, []telego.InlineKeyboardButton{button("Участник "+number, "detail", i)})
	}
	if nav := professionPageButtons(button, "peoplePage", offset, pageSize, len(ids)); len(nav) != 0 {
		rows = append(rows, nav)
	}
	rows = append(rows,
		[]telego.InlineKeyboardButton{button("Показать список", "peoplePage", offset)},
		[]telego.InlineKeyboardButton{button("По профессиям", "by", 0), button("К меню", "home", 0)},
	)
	if len(summary) == 0 {
		summary = append(summary, "На этой странице нет доступных участников.")
	}
	return fmt.Sprintf("%s · Страница %d", title, offset/pageSize+1), rows, strings.Join(summary, "\n"), nil
}

func professionBoundPage(offset, count, size int) int {
	return min(offset, max(0, count-1)/size*size)
}

func (m *professionsMenu) detail(ctx context.Context, s *professionSession, button func(string, string, int) telego.InlineKeyboardButton) ([][]telego.InlineKeyboardButton, string, error) {
	rows := [][]telego.InlineKeyboardButton{{button("К списку участников", "backPeople", 0)}}
	member := m.presentMember(ctx, s.chat, s.detailID)
	if member == nil {
		return rows, "Участник больше недоступен.", nil
	}
	roles, err := m.currentChoice(s.chat, s.detailID, professionRoles)
	if err != nil {
		return nil, "", err
	}
	stack, err := m.currentChoice(s.chat, s.detailID, professionStack)
	if err != nil {
		return nil, "", err
	}
	var lines []string
	if len(roles) == 0 {
		lines = append(lines, "Профессии: не указаны")
	} else {
		for _, name := range roles {
			lines = append(lines, "Профессия: "+name)
		}
	}
	if len(stack) == 0 {
		lines = append(lines, "Стек: не указан")
	} else {
		for _, name := range stack {
			lines = append(lines, "Стек: "+name)
		}
	}
	const pageSize = 3
	offset := professionBoundPage(s.offset, len(lines), pageSize)
	if nav := professionPageButtons(button, "detailPage", offset, pageSize, len(lines)); len(nav) != 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, []telego.InlineKeyboardButton{button("Показать профиль", "detailPage", offset)})
	var summary strings.Builder
	summary.WriteString(professionLimit(professionMemberLabel(*member), 38))
	for _, line := range lines[offset:min(offset+pageSize, len(lines))] {
		summary.WriteByte('\n')
		summary.WriteString(professionLimit(line, 50))
	}
	return rows, summary.String(), nil
}

func professionMemberLabel(user telego.User) string {
	if user.Username != "" {
		return "@" + user.Username
	}
	if name := strings.Join(strings.Fields(user.FirstName+" "+user.LastName), " "); name != "" {
		return name
	}
	return fmt.Sprintf("Участник %d", user.ID)
}

func professionRoleLabel(roles []string) string {
	if len(roles) == 0 {
		return ""
	}
	return " - " + strings.Join(roles, ", ")
}

func professionLimit(text string, limit int) string {
	const ellipsis = "..."
	if inactivityTextUnits(text) <= limit {
		return text
	}
	limit -= len(ellipsis)
	for i, r := range text {
		limit -= inactivityRuneUnits(r)
		if limit < 0 {
			return text[:i] + ellipsis
		}
	}
	return text
}
