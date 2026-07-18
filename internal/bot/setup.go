package bot

import (
	"context"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

func setCommands(ctx context.Context, bot *telego.Bot) error {
	// Group + admin-in-group menus expose the public, non-moderation
	// surface (read-only stats + games + the referral catalog). There
	// is no private management console anymore; the bot only responds
	// to /help and /start in DMs.
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
		{Command: "help", Description: "Справка"},
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

	scopes := []struct {
		commands []telego.BotCommand
		scope    telego.BotCommandScope
	}{
		{groupCommands, tu.ScopeAllGroupChats()},
		{adminCommands, tu.ScopeAllChatAdministrators()},
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
