package bot

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	"github.com/mymmrac/telego/telegoapi"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/domain/membership"
)

const ownerChatsCallbackPrefix = "oc:"

func (a *App) handleOwnerChats(_ *th.Context, msg telego.Message) error {
	if msg.From == nil {
		return nil
	}
	if msg.From.ID != a.botOwnerID {
		_, err := a.sender.SendMessage(context.Background(), &telego.SendMessageParams{
			ChatID: telego.ChatID{ID: msg.Chat.ID},
			Text:   "Команда доступна только владельцу бота.",
		})
		return err
	}

	chats, err := a.memberSvc.Store().ListChats(context.Background())
	if err != nil {
		a.log.Error("list owner chats failed", "error", err)
		_, sendErr := a.sender.SendMessage(context.Background(), &telego.SendMessageParams{
			ChatID: telego.ChatID{ID: msg.Chat.ID},
			Text:   "Не удалось прочитать список чатов.",
		})
		return sendErr
	}

	active := a.verifiedCurrentBotChats(context.Background(), chats)
	if len(active) == 0 {
		_, err = a.sender.SendMessage(context.Background(), &telego.SendMessageParams{
			ChatID: telego.ChatID{ID: msg.Chat.ID},
			Text:   "Бот сейчас не состоит ни в одном чате.",
		})
		return err
	}

	var text strings.Builder
	text.WriteString("Чаты, где бот сейчас работает:\n")
	rows := make([][]telego.InlineKeyboardButton, 0, len(active))
	for i, chat := range active {
		name := ownerChatName(chat)
		fmt.Fprintf(&text, "\n%d. %s\n   ID: -%d\n   Статус: %s\n", i+1, name, chat.AbsChatID, chat.BotStatus)
		rows = append(rows, []telego.InlineKeyboardButton{{
			Text:         "Отозвать · " + name,
			CallbackData: fmt.Sprintf("%sask:%d", ownerChatsCallbackPrefix, chat.AbsChatID),
		}})
	}

	_, err = a.sender.SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: msg.Chat.ID},
		Text:   strings.TrimSpace(text.String()),
		ReplyMarkup: &telego.InlineKeyboardMarkup{
			InlineKeyboard: rows,
		},
	})
	return err
}

func currentBotChats(chats []membership.Chat) []membership.Chat {
	active := make([]membership.Chat, 0, len(chats))
	for _, chat := range chats {
		if chat.Type == telego.ChatTypeSupergroup && botStatusIsCurrent(chat.BotStatus) {
			active = append(active, chat)
		}
	}
	sort.Slice(active, func(i, j int) bool {
		left := strings.ToLower(ownerChatName(active[i]))
		right := strings.ToLower(ownerChatName(active[j]))
		if left == right {
			return active[i].AbsChatID < active[j].AbsChatID
		}
		return left < right
	})
	return active
}

func (a *App) verifiedCurrentBotChats(ctx context.Context, chats []membership.Chat) []membership.Chat {
	active := currentBotChats(chats)
	botInfo, err := a.sender.GetMe(ctx)
	if err != nil {
		a.log.Warn("resolve bot identity for owner chats failed", "error", err)
		return active
	}

	verified := make([]membership.Chat, 0, len(active))
	for _, chat := range active {
		member, err := a.sender.GetChatMember(ctx, &telego.GetChatMemberParams{
			ChatID: telego.ChatID{ID: -chat.AbsChatID},
			UserID: botInfo.ID,
		})
		switch {
		case err == nil && member.MemberStatus() != telego.MemberStatusLeft &&
			member.MemberStatus() != telego.MemberStatusBanned:
			verified = append(verified, chat)
		case err == nil || telegramChatUnavailable(err):
			a.markOwnerChatLeft(chat.AbsChatID)
			a.log.Info("removed stale owner chat", "chat_id", -chat.AbsChatID, "error", err)
		default:
			a.log.Warn("verify owner chat failed", "chat_id", -chat.AbsChatID, "error", err)
			verified = append(verified, chat)
		}
	}
	return verified
}

func botStatusIsCurrent(status membership.Status) bool {
	switch status {
	case membership.StatusCreator, membership.StatusAdministrator, membership.StatusMember, membership.StatusRestricted:
		return true
	default:
		return false
	}
}

func ownerChatName(chat membership.Chat) string {
	if title := strings.TrimSpace(chat.Title); title != "" {
		return title
	}
	return fmt.Sprintf("Chat -%d", chat.AbsChatID)
}

func ownerChatsCallbackPredicate() th.Predicate {
	return func(_ context.Context, update telego.Update) bool {
		query := update.CallbackQuery
		if query == nil || query.Message == nil {
			return false
		}
		return query.Message.GetChat().Type == telego.ChatTypePrivate &&
			strings.HasPrefix(query.Data, ownerChatsCallbackPrefix)
	}
}

func (a *App) handleOwnerChatsCallback(_ *th.Context, query telego.CallbackQuery) error {
	answer := ""
	showAlert := false
	defer func() {
		_ = a.sender.AnswerCallbackQuery(context.Background(), &telego.AnswerCallbackQueryParams{
			CallbackQueryID: query.ID,
			Text:            answer,
			ShowAlert:       showAlert,
		})
	}()

	if query.Message == nil || query.Message.GetChat().Type != telego.ChatTypePrivate {
		answer = "Эта кнопка работает только в личке с ботом."
		showAlert = true
		return nil
	}
	if query.From.ID != a.botOwnerID {
		answer = "Действие доступно только владельцу бота."
		showAlert = true
		return nil
	}

	action, absChatID, ok := parseOwnerChatsCallback(query.Data)
	if !ok {
		answer = "Кнопка устарела. Запустите /chats заново."
		showAlert = true
		return nil
	}

	if action == "cancel" {
		if err := a.editOwnerChatsMessage(query, "Отзыв отменён.\n\nЗапустите /chats, чтобы вернуться к списку.", emptyKeyboard()); err != nil {
			a.log.Warn("edit owner chat cancellation failed", "error", err)
			answer = "Отзыв отменён, но сообщение не обновилось."
			showAlert = true
			return nil
		}
		answer = "Отменено."
		return nil
	}

	chat, err := a.memberSvc.Store().GetChat(context.Background(), absChatID)
	if err != nil || chat.Type != telego.ChatTypeSupergroup || !botStatusIsCurrent(chat.BotStatus) {
		answer = "Бот уже не состоит в этом чате. Обновите список командой /chats."
		showAlert = true
		return nil
	}

	switch action {
	case "ask":
		text := fmt.Sprintf(
			"Подтвердить отзыв бота из чата?\n\nЧат: %s\nID: -%d\n\nПосле подтверждения бот покинет чат.",
			ownerChatName(*chat),
			chat.AbsChatID,
		)
		keyboard := &telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{{
			{Text: "Да, отозвать", CallbackData: fmt.Sprintf("%sleave:%d", ownerChatsCallbackPrefix, chat.AbsChatID)},
			{Text: "Отмена", CallbackData: fmt.Sprintf("%scancel:%d", ownerChatsCallbackPrefix, chat.AbsChatID)},
		}}}
		if err := a.editOwnerChatsMessage(query, text, keyboard); err != nil {
			a.log.Warn("edit owner chat confirmation failed", "error", err, "chat_id", -chat.AbsChatID)
			answer = "Не удалось открыть подтверждение. Попробуйте ещё раз."
			showAlert = true
		}
	case "leave":
		var leaveErr error
		leaveErr = a.leaver.LeaveChat(context.Background(), &telego.LeaveChatParams{
			ChatID: telego.ChatID{ID: -chat.AbsChatID},
		})
		if leaveErr != nil {
			if telegramChatUnavailable(leaveErr) {
				a.markOwnerChatLeft(chat.AbsChatID)
				text := fmt.Sprintf("Бот уже не состоит в чате %s.\n\nID: -%d\nЗапись удалена из списка.", ownerChatName(*chat), chat.AbsChatID)
				if err := a.editOwnerChatsMessage(query, text, emptyKeyboard()); err != nil {
					a.log.Warn("edit stale owner chat result failed", "error", err, "chat_id", -chat.AbsChatID)
				}
				answer = "Бота уже нет в этом чате. Запись удалена."
				return nil
			}
			a.log.Warn("owner chat revoke failed", "error", leaveErr, "chat_id", -chat.AbsChatID)
			answer = "Telegram не позволил выйти из чата. Попробуйте ещё раз."
			showAlert = true
			return nil
		}
		if err := a.memberSvc.MarkBotLeft(context.Background(), chat.AbsChatID, time.Now().UTC()); err != nil {
			a.log.Error("mark owner-revoked chat left failed", "error", err, "chat_id", -chat.AbsChatID)
		}
		text := fmt.Sprintf("Бот вышел из чата %s.\n\nID: -%d", ownerChatName(*chat), chat.AbsChatID)
		if err := a.editOwnerChatsMessage(query, text, emptyKeyboard()); err != nil {
			a.log.Warn("edit owner chat revoke result failed", "error", err, "chat_id", -chat.AbsChatID)
			answer = "Бот вышел, но сообщение не обновилось."
			showAlert = true
			return nil
		}
		answer = "Бот отозван."
	}
	return nil
}

func (a *App) markOwnerChatLeft(absChatID int64) {
	if err := a.memberSvc.MarkBotLeft(context.Background(), absChatID, time.Now().UTC()); err != nil {
		a.log.Error("mark owner chat left failed", "error", err, "chat_id", -absChatID)
	}
}

func telegramChatUnavailable(err error) bool {
	var apiErr *telegoapi.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	description := strings.ToLower(apiErr.Description)
	return strings.Contains(description, "chat not found") ||
		strings.Contains(description, "bot was kicked") ||
		strings.Contains(description, "bot is not a member")
}

func parseOwnerChatsCallback(data string) (string, int64, bool) {
	parts := strings.Split(data, ":")
	if len(parts) != 3 || parts[0] != strings.TrimSuffix(ownerChatsCallbackPrefix, ":") {
		return "", 0, false
	}
	switch parts[1] {
	case "ask", "leave", "cancel":
	default:
		return "", 0, false
	}
	absChatID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || absChatID <= 0 {
		return "", 0, false
	}
	return parts[1], absChatID, true
}

func (a *App) editOwnerChatsMessage(query telego.CallbackQuery, text string, keyboard *telego.InlineKeyboardMarkup) error {
	_, err := a.sender.EditMessageText(context.Background(), &telego.EditMessageTextParams{
		ChatID:      telego.ChatID{ID: query.Message.GetChat().ID},
		MessageID:   query.Message.GetMessageID(),
		Text:        text,
		ReplyMarkup: keyboard,
	})
	return err
}
