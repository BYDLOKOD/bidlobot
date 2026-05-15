package testutil

import (
	"context"
	"fmt"
	"sync"

	"github.com/mymmrac/telego"
)

type SentMessage struct {
	ChatID    int64
	Text      string
	ParseMode string
	ReplyTo   int
	Keyboard  *telego.InlineKeyboardMarkup
}

type APICall struct {
	Method string
	Params any
}

type MockAPI struct {
	mu       sync.Mutex
	Messages []SentMessage
	Calls    []APICall

	AdminIDs        map[int64][]int64
	BotCanRestrict  bool
	ChatMembers     map[string]string      // "chatID:userID" -> status
	ChatMemberUsers map[string]telego.User // "chatID:userID" -> identity GetChatMember returns
	ChatMemberErrs  map[string]error       // "chatID:userID" -> GetChatMember error
	ChatPerms       *telego.ChatPermissions
	BotInfo         *telego.User
}

func NewMockAPI() *MockAPI {
	return &MockAPI{
		AdminIDs:        make(map[int64][]int64),
		BotCanRestrict:  true,
		ChatMembers:     make(map[string]string),
		ChatMemberUsers: make(map[string]telego.User),
		ChatMemberErrs:  make(map[string]error),
		BotInfo:         &telego.User{ID: 999, Username: "test_bot", IsBot: true},
	}
}

func (m *MockAPI) SendMessage(_ context.Context, params *telego.SendMessageParams) (*telego.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var kb *telego.InlineKeyboardMarkup
	if params.ReplyMarkup != nil {
		if ikb, ok := params.ReplyMarkup.(*telego.InlineKeyboardMarkup); ok {
			kb = ikb
		}
	}

	var replyTo int
	if params.ReplyParameters != nil {
		replyTo = params.ReplyParameters.MessageID
	}

	m.Messages = append(m.Messages, SentMessage{
		ChatID:    params.ChatID.ID,
		Text:      params.Text,
		ParseMode: params.ParseMode,
		ReplyTo:   replyTo,
		Keyboard:  kb,
	})
	m.Calls = append(m.Calls, APICall{"SendMessage", params})

	return &telego.Message{MessageID: len(m.Messages), Chat: telego.Chat{ID: params.ChatID.ID}}, nil
}

func (m *MockAPI) GetChatAdministrators(_ context.Context, params *telego.GetChatAdministratorsParams) ([]telego.ChatMember, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, APICall{"GetChatAdministrators", params})

	chatID := params.ChatID.ID
	if chatID < 0 {
		chatID = -chatID
	}

	var members []telego.ChatMember
	for _, uid := range m.AdminIDs[chatID] {
		admin := &telego.ChatMemberAdministrator{
			User:               telego.User{ID: uid},
			CanRestrictMembers: m.BotCanRestrict,
		}
		members = append(members, admin)
	}

	if m.BotInfo != nil {
		botAdmin := &telego.ChatMemberAdministrator{
			User:               *m.BotInfo,
			CanRestrictMembers: m.BotCanRestrict,
		}
		members = append(members, botAdmin)
	}

	return members, nil
}

func (m *MockAPI) GetChatMember(_ context.Context, params *telego.GetChatMemberParams) (telego.ChatMember, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, APICall{"GetChatMember", params})

	key := chatMemberKey(params.ChatID.ID, params.UserID)
	if err, ok := m.ChatMemberErrs[key]; ok && err != nil {
		return nil, err
	}
	status, ok := m.ChatMembers[key]
	if !ok {
		status = "member"
	}
	usr := telego.User{ID: params.UserID}
	if u, ok := m.ChatMemberUsers[key]; ok {
		usr = u
		usr.ID = params.UserID
	}

	switch status {
	case "kicked":
		return &telego.ChatMemberBanned{User: usr}, nil
	case "left":
		return &telego.ChatMemberLeft{User: usr}, nil
	case "administrator":
		return &telego.ChatMemberAdministrator{User: usr}, nil
	case "creator":
		return &telego.ChatMemberOwner{User: usr}, nil
	default:
		return &telego.ChatMemberMember{User: usr}, nil
	}
}

func (m *MockAPI) GetChat(_ context.Context, params *telego.GetChatParams) (*telego.ChatFullInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, APICall{"GetChat", params})

	return &telego.ChatFullInfo{
		ID:          params.ChatID.ID,
		Type:        telego.ChatTypeSupergroup,
		Permissions: m.ChatPerms,
	}, nil
}

func (m *MockAPI) RestrictChatMember(_ context.Context, params *telego.RestrictChatMemberParams) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, APICall{"RestrictChatMember", params})
	return nil
}

func (m *MockAPI) BanChatMember(_ context.Context, params *telego.BanChatMemberParams) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, APICall{"BanChatMember", params})
	return nil
}

func (m *MockAPI) UnbanChatMember(_ context.Context, params *telego.UnbanChatMemberParams) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, APICall{"UnbanChatMember", params})
	return nil
}

func (m *MockAPI) DeleteMessage(_ context.Context, params *telego.DeleteMessageParams) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, APICall{"DeleteMessage", params})
	return nil
}

func (m *MockAPI) AnswerCallbackQuery(_ context.Context, params *telego.AnswerCallbackQueryParams) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, APICall{"AnswerCallbackQuery", params})
	return nil
}

func (m *MockAPI) GetMe(_ context.Context) (*telego.User, error) {
	return m.BotInfo, nil
}

func (m *MockAPI) LastMessage() *SentMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.Messages) == 0 {
		return nil
	}
	return &m.Messages[len(m.Messages)-1]
}

func (m *MockAPI) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Messages = nil
	m.Calls = nil
}

func (m *MockAPI) CallCount(method string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.Calls {
		if c.Method == method {
			n++
		}
	}
	return n
}

func chatMemberKey(chatID, userID int64) string {
	if chatID < 0 {
		chatID = -chatID
	}
	return fmt.Sprintf("%d:%d", chatID, userID)
}
