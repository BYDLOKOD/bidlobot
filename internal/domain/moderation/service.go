package moderation

import (
	"html"
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/mymmrac/telego"
	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/text"
)

type Service struct {
	store Store
	api   shared.TelegramAPI
	admin *shared.AdminCache
	log   *slog.Logger
}

func NewService(store Store, api shared.TelegramAPI, admin *shared.AdminCache, log *slog.Logger) *Service {
	return &Service{
		store: store,
		api:   api,
		admin: admin,
		log:   log,
	}
}

// Warn создаёт предупреждение и возвращает количество активных.
func (s *Service) Warn(ctx context.Context, chatID int64, targetUserID int64, issuerUserID int64, reason string) (activeCount int, err error) {
	w := &Warning{
		ID:           uuid.New().String(),
		TargetUserID: targetUserID,
		ChatID:       chatID,
		IssuerUserID: issuerUserID,
		Reason:       reason,
		Timestamp:    time.Now().UTC(),
		Active:       true,
	}
	return s.store.CreateWarning(ctx, w)
}

// AutoMute ограничивает права члена на 24 часа.
func (s *Service) AutoMute(ctx context.Context, chatID, targetUserID int64) error {
	falseVal := false
	params := &telego.RestrictChatMemberParams{
		ChatID: telego.ChatID{ID: chatID},
		UserID: targetUserID,
		Permissions: telego.ChatPermissions{
			CanSendMessages:       &falseVal,
			CanSendAudios:         &falseVal,
			CanSendDocuments:      &falseVal,
			CanSendPhotos:         &falseVal,
			CanSendVideos:         &falseVal,
			CanSendVideoNotes:     &falseVal,
			CanSendVoiceNotes:     &falseVal,
			CanSendPolls:          &falseVal,
			CanSendOtherMessages:  &falseVal,
			CanAddWebPagePreviews: &falseVal,
			CanChangeInfo:         &falseVal,
			CanInviteUsers:        &falseVal,
			CanPinMessages:        &falseVal,
			CanManageTopics:       &falseVal,
		},
		UntilDate: int64(time.Now().Add(24 * time.Hour).Unix()),
	}
	return s.api.RestrictChatMember(ctx, params)
}

// ListWarnings форматирует список предупреждений.
func (s *Service) ListWarnings(ctx context.Context, targetUserID, absChatID int64) (string, error) {
	warnings, err := s.store.ListActive(ctx, targetUserID, absChatID)
	if err != nil {
		return "", err
	}

	if len(warnings) == 0 {
		return "Предупреждений нет.", nil
	}

	var result string
	if len(warnings) < 4 {
		result = fmt.Sprintf("Предупреждения (%d/3)\n", len(warnings))
	} else {
		result = fmt.Sprintf("Предупреждения (всего %d)\n", len(warnings))
	}

	for i, w := range warnings {
		issuerDisplay := shared.UserDisplay(s.resolveUsername(w.IssuerUserID), fmt.Sprintf("user_%d", w.IssuerUserID))
		dateStr := shared.FormatDate(w.Timestamp)
		reason := w.Reason
		if reason == "" {
			reason = "(без причины)"
		}
		result += fmt.Sprintf("%d. %s - выдал %s, %s\n", i+1, html.EscapeString(reason), issuerDisplay, dateStr)
	}

	return result, nil
}

// ClearWarnings удаляет все предупреждения.
func (s *Service) ClearWarnings(ctx context.Context, targetUserID, absChatID int64) error {
	return s.store.ClearWarnings(ctx, targetUserID, absChatID)
}

// Mute ограничивает права члена на указанную длительность.
func (s *Service) Mute(ctx context.Context, chatID, targetUserID int64, duration time.Duration) error {
	falseVal := false
	params := &telego.RestrictChatMemberParams{
		ChatID: telego.ChatID{ID: chatID},
		UserID: targetUserID,
		Permissions: telego.ChatPermissions{
			CanSendMessages:       &falseVal,
			CanSendAudios:         &falseVal,
			CanSendDocuments:      &falseVal,
			CanSendPhotos:         &falseVal,
			CanSendVideos:         &falseVal,
			CanSendVideoNotes:     &falseVal,
			CanSendVoiceNotes:     &falseVal,
			CanSendPolls:          &falseVal,
			CanSendOtherMessages:  &falseVal,
			CanAddWebPagePreviews: &falseVal,
			CanChangeInfo:         &falseVal,
			CanInviteUsers:        &falseVal,
			CanPinMessages:        &falseVal,
			CanManageTopics:       &falseVal,
		},
		UntilDate: int64(time.Now().Add(duration).Unix()),
	}
	return s.api.RestrictChatMember(ctx, params)
}

// Unmute восстанавливает права члена.
func (s *Service) Unmute(ctx context.Context, chatID, targetUserID int64) error {
	chat, err := s.api.GetChat(ctx, &telego.GetChatParams{
		ChatID: telego.ChatID{ID: chatID},
	})
	if err != nil {
		return err
	}

	var perms telego.ChatPermissions
	if chat.Permissions != nil {
		perms = *chat.Permissions
	} else {
		// Если нет ограничений по умолчанию, разрешаем всё
		trueVal := true
		perms = telego.ChatPermissions{
			CanSendMessages:       &trueVal,
			CanSendAudios:         &trueVal,
			CanSendDocuments:      &trueVal,
			CanSendPhotos:         &trueVal,
			CanSendVideos:         &trueVal,
			CanSendVideoNotes:     &trueVal,
			CanSendVoiceNotes:     &trueVal,
			CanSendPolls:          &trueVal,
			CanSendOtherMessages:  &trueVal,
			CanAddWebPagePreviews: &trueVal,
			CanChangeInfo:         &trueVal,
			CanInviteUsers:        &trueVal,
			CanPinMessages:        &trueVal,
			CanManageTopics:       &trueVal,
		}
	}

	return s.api.RestrictChatMember(ctx, &telego.RestrictChatMemberParams{
		ChatID:      telego.ChatID{ID: chatID},
		UserID:      targetUserID,
		Permissions: perms,
	})
}

// Ban блокирует члена чата.
func (s *Service) Ban(ctx context.Context, chatID, targetUserID int64) error {
	return s.api.BanChatMember(ctx, &telego.BanChatMemberParams{
		ChatID:         telego.ChatID{ID: chatID},
		UserID:         targetUserID,
		RevokeMessages: false,
	})
}

// Unban разблокирует члена чата, если он был заблокирован.
func (s *Service) Unban(ctx context.Context, chatID, targetUserID int64) error {
	member, err := s.api.GetChatMember(ctx, &telego.GetChatMemberParams{
		ChatID: telego.ChatID{ID: chatID},
		UserID: targetUserID,
	})
	if err != nil {
		return err
	}

	if member.MemberStatus() != "kicked" {
		return fmt.Errorf("%s", text.ErrUserNotBanned)
	}

	return s.api.UnbanChatMember(ctx, &telego.UnbanChatMemberParams{
		ChatID:       telego.ChatID{ID: chatID},
		UserID:       targetUserID,
		OnlyIfBanned: true,
	})
}

// ValidateTarget проверяет возможность действия над целевым пользователем.
func (s *Service) ValidateTarget(ctx context.Context, absChatID, callerID, targetUserID int64, targetIsBot bool, action string) error {
	// Нельзя действовать над ботом
	if targetIsBot {
		return fmt.Errorf(text.ErrCantActionBot, action)
	}

	// Нельзя действовать над собой
	if callerID == targetUserID {
		return fmt.Errorf(text.ErrCantActionSelf, action)
	}

	// Нельзя действовать над администратором
	isAdmin, err := s.admin.IsAdmin(absChatID, targetUserID)
	if err != nil {
		return err
	}
	if isAdmin {
		return fmt.Errorf(text.ErrCantActionAdmin, action)
	}

	return nil
}

// resolveUsername пытается получить имя пользователя по ID (заглушка для интеграции с реальной БД).
func (s *Service) resolveUsername(userID int64) string {
	return ""
}
