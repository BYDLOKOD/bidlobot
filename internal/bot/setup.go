package bot

import (
	"context"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

func setCommands(ctx context.Context, bot *telego.Bot) error {
	// Moderation lives ONLY in the private menu. Advertising /ban etc.
	// in the group's command menu invited admins to type them in the
	// chat (where the bot now just deletes+redirects them) - the menu
	// must not suggest the very fumble the privacy rework removes.
	privateCommands := []telego.BotCommand{
		{Command: "start", Description: "Выбрать чат для управления"},
		{Command: "chat", Description: "Сменить активный чат"},
		{Command: "stats", Description: "Статистика чата"},
		{Command: "warn", Description: "Предупредить участника"},
		{Command: "warns", Description: "Предупреждения / сброс"},
		{Command: "mute", Description: "Замьютить участника"},
		{Command: "unmute", Description: "Размьютить участника"},
		{Command: "ban", Description: "Забанить участника"},
		{Command: "unban", Description: "Разбанить участника"},
		{Command: "cleanup", Description: "Чистка неактивных"},
		{Command: "help", Description: "Справка"},
	}

	// Group + admin-in-group menus expose ONLY the public,
	// non-moderation surface (read-only stats + games).
	groupCommands := []telego.BotCommand{
		{Command: "stats", Description: "Статистика чата"},
		{Command: "dice", Description: "Бросить кубик"},
		{Command: "battle", Description: "Реакция-баттл X vs Y"},
		{Command: "quiz", Description: "Угадай язык по коду"},
		{Command: "help", Description: "Справка"},
	}

	scopes := []struct {
		commands []telego.BotCommand
		scope    telego.BotCommandScope
	}{
		{privateCommands, tu.ScopeAllPrivateChats()},
		{groupCommands, tu.ScopeAllGroupChats()},
		{groupCommands, tu.ScopeAllChatAdministrators()},
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
