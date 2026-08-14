package bot

import (
	"context"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

func setCommands(ctx context.Context, bot *telego.Bot, ownerID int64) error {
	// Group menus expose the public surface. The installation console is
	// published only in the owner's private chat.
	groupCommands := []telego.BotCommand{
		{Command: "stats", Description: "Статистика чата (top/today/month)"},
		{Command: "summarize", Description: "Итог последних N сообщений (для админов)"},
		{Command: "dice", Description: "Бросить кубик"},
		{Command: "battle", Description: "Реакция-баттл X vs Y"},
		{Command: "quiz", Description: "Угадай язык по коду"},
		{Command: "poll", Description: "Опрос: вопрос | вар1 | вар2"},
		{Command: "8ball", Description: "Спросить шар предсказаний"},
		{Command: "roast", Description: "Поджарить участника"},
		{Command: "praise", Description: "Похвалить участника"},
		{Command: "rep", Description: "Reputation balance"},
		{Command: "reptop", Description: "Reputation top"},
		{Command: "guess", Description: "Угадай число 1-100"},
		{Command: "hangman", Description: "Виселица (IT-слова)"},
		{Command: "duel", Description: "Дуэль: /duel @user"},
		{Command: "trivia", Description: "IT-викторина"},
		{Command: "refs", Description: "Реферальные ссылки чата"},
		{Command: "refreg", Description: "Добавить реферальную ссылку"},
		{Command: "flush", Description: "Повторить неудачные запросы"},
		{Command: "help", Description: "Список команд (в личку)"},
	}

	// Administrator scope = the public surface plus the moderation
	// tool. /refreport is admin-only and never appears in the regular
	// member menu.
	adminCommands := make([]telego.BotCommand, len(groupCommands))
	copy(adminCommands, groupCommands)
	adminCommands = append(adminCommands, telego.BotCommand{
		Command:     "refreport",
		Description: "Удалить рефку по ID",
	})

	ownerCommands := []telego.BotCommand{
		{Command: "start", Description: "Справка"},
		{Command: "help", Description: "Справка"},
		{Command: "chats", Description: "Чаты, где работает бот"},
	}

	scopes := []struct {
		commands []telego.BotCommand
		scope    telego.BotCommandScope
	}{
		{groupCommands, tu.ScopeAllGroupChats()},
		{adminCommands, tu.ScopeAllChatAdministrators()},
	}
	if ownerID != 0 {
		scopes = append(scopes, struct {
			commands []telego.BotCommand
			scope    telego.BotCommandScope
		}{ownerCommands, tu.ScopeChat(telego.ChatID{ID: ownerID})})
	}

	for _, s := range scopes {
		if err := bot.SetMyCommands(ctx, &telego.SetMyCommandsParams{
			Commands: s.commands,
			Scope:    s.scope,
		}); err != nil {
			return err
		}
	}
	return nil
}
