package shared

import (
	"context"

	"github.com/mymmrac/telego"
)

type TelegramAPI interface {
	SendMessage(ctx context.Context, params *telego.SendMessageParams) (*telego.Message, error)
	EditMessageText(ctx context.Context, params *telego.EditMessageTextParams) (*telego.Message, error)
	GetChatAdministrators(ctx context.Context, params *telego.GetChatAdministratorsParams) ([]telego.ChatMember, error)
	GetChatMember(ctx context.Context, params *telego.GetChatMemberParams) (telego.ChatMember, error)
	GetChat(ctx context.Context, params *telego.GetChatParams) (*telego.ChatFullInfo, error)
	RestrictChatMember(ctx context.Context, params *telego.RestrictChatMemberParams) error
	BanChatMember(ctx context.Context, params *telego.BanChatMemberParams) error
	UnbanChatMember(ctx context.Context, params *telego.UnbanChatMemberParams) error
	DeleteMessage(ctx context.Context, params *telego.DeleteMessageParams) error
	AnswerCallbackQuery(ctx context.Context, params *telego.AnswerCallbackQueryParams) error
	GetMe(ctx context.Context) (*telego.User, error)
}

// DisplayResolver returns a chat-local display name for a user.
// A nil resolver is tolerated - callers fall back to "User <id>".
type DisplayResolver interface {
	UserDisplay(ctx context.Context, absChatID, userID int64) string
}
