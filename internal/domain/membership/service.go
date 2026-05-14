package membership

import (
	"context"
	"log/slog"
	"time"

	"github.com/mymmrac/telego"
)

// Service holds the high-level operations bot handlers invoke. Wraps Store
// with Telegram-shaped inputs (telego.Message, telego.User, etc.) so that
// individual handlers stay shallow.
type Service struct {
	store Store
	log   *slog.Logger
}

func NewService(store Store, log *slog.Logger) *Service {
	return &Service{store: store, log: log}
}

func (s *Service) Store() Store { return s.store }

// RecordMessage upserts the sender of a non-service message: bumps
// MessageCount, refreshes LastMessageAt, syncs Username/FirstName,
// records IsBot/IsPremium flags. Service messages, anonymous admin
// messages and channel auto-forwards must be filtered upstream - this
// method trusts that the caller already validated `from`.
func (s *Service) RecordMessage(ctx context.Context, absChatID int64, from *telego.User, ts time.Time) error {
	if from == nil || from.ID == 0 || absChatID == 0 {
		return nil
	}
	username := from.Username
	firstName := from.FirstName
	isBot := from.IsBot
	isPremium := from.IsPremium
	_, err := s.store.UpsertMember(ctx, MemberPatch{
		UserID:          from.ID,
		AbsChatID:       absChatID,
		Username:        &username,
		FirstName:       &firstName,
		IsBot:           &isBot,
		IsPremium:       &isPremium,
		Status:          StatusMember,
		KnownVia:        SourceMessage,
		LastMessageAt:   ts,
		IncMessageCount: 1,
		Now:             ts,
	})
	return err
}

// RecordReaction handles a message_reaction update. Anonymous reactions
// (User == nil) and reactions issued by the bot itself are ignored.
// Both adding and removing a reaction count as user activity for cleanup
// purposes - the user is provably alive in the chat.
func (s *Service) RecordReaction(ctx context.Context, reaction telego.MessageReactionUpdated) error {
	user := reaction.User
	if user == nil || user.IsBot {
		return nil
	}
	if user.ID == 0 || reaction.Chat.ID == 0 {
		return nil
	}
	absChatID := absChatID(reaction.Chat.ID)
	ts := time.Unix(reaction.Date, 0).UTC()

	username := user.Username
	firstName := user.FirstName
	isBot := user.IsBot
	isPremium := user.IsPremium

	_, err := s.store.UpsertMember(ctx, MemberPatch{
		UserID:           user.ID,
		AbsChatID:        absChatID,
		Username:         &username,
		FirstName:        &firstName,
		IsBot:            &isBot,
		IsPremium:        &isPremium,
		KnownVia:         SourceReaction,
		LastReactionAt:   ts,
		IncReactionCount: 1,
		Now:              ts,
	})
	return err
}

// RecordChatMember handles a chat_member update - a status change for a
// user in the chat (join, leave, ban, restrict, promote). Drives the
// Status field and the JoinedAt/LeftAt timestamps.
func (s *Service) RecordChatMember(ctx context.Context, cmu telego.ChatMemberUpdated) error {
	newMember := cmu.NewChatMember
	user := newMember.MemberUser()
	if user.ID == 0 || cmu.Chat.ID == 0 {
		return nil
	}
	absChatID := absChatID(cmu.Chat.ID)
	ts := time.Unix(cmu.Date, 0).UTC()

	status := mapStatus(newMember.MemberStatus())

	patch := MemberPatch{
		UserID:    user.ID,
		AbsChatID: absChatID,
		Username:  ptr(user.Username),
		FirstName: ptr(user.FirstName),
		IsBot:     ptr(user.IsBot),
		IsPremium: ptr(user.IsPremium),
		Status:    status,
		KnownVia:  SourceChatMember,
		Now:       ts,
	}
	switch status {
	case StatusMember, StatusAdministrator, StatusCreator, StatusRestricted:
		patch.JoinedAt = ts
	case StatusLeft, StatusKicked:
		patch.LeftAt = ts
	}

	_, err := s.store.UpsertMember(ctx, patch)
	return err
}

// RecordMyChatMember handles a my_chat_member update - a status change
// for the bot itself. We use it to register the chat in the chats bucket
// and remember whether we have the rights needed for moderation.
func (s *Service) RecordMyChatMember(ctx context.Context, cmu telego.ChatMemberUpdated) error {
	if cmu.Chat.ID == 0 {
		return nil
	}
	absChatID := absChatID(cmu.Chat.ID)
	newMember := cmu.NewChatMember
	status := mapStatus(newMember.MemberStatus())
	canRestrict := false
	canDelete := false
	if admin, ok := newMember.(*telego.ChatMemberAdministrator); ok {
		canRestrict = admin.CanRestrictMembers
		canDelete = admin.CanDeleteMessages
	}
	return s.store.UpsertChat(ctx, Chat{
		AbsChatID:    absChatID,
		Title:        cmu.Chat.Title,
		Type:         string(cmu.Chat.Type),
		BotStatus:    status,
		CanRestrict:  canRestrict,
		CanDelete:    canDelete,
		LastUpdateAt: time.Unix(cmu.Date, 0).UTC(),
	})
}

func mapStatus(s string) Status {
	switch Status(s) {
	case StatusCreator, StatusAdministrator, StatusMember, StatusRestricted, StatusLeft, StatusKicked:
		return Status(s)
	default:
		return StatusUnknown
	}
}

func absChatID(chatID int64) int64 {
	if chatID < 0 {
		return -chatID
	}
	return chatID
}

func ptr[T any](v T) *T { return &v }
