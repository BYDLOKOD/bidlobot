package bot

import (
	"context"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

func setCommands(ctx context.Context, bot *telego.Bot) error {
	privateCommands := []telego.BotCommand{
		{Command: "help", Description: "Справка"},
	}

	groupCommands := []telego.BotCommand{
		{Command: "stats", Description: "Статистика чата"},
		{Command: "help", Description: "Справка"},
	}

	adminCommands := []telego.BotCommand{
		{Command: "stats", Description: "Статистика чата"},
		{Command: "warn", Description: "Предупредить пользователя"},
		{Command: "warns", Description: "Предупреждения / сброс"},
		{Command: "mute", Description: "Замьютить пользователя"},
		{Command: "unmute", Description: "Размьютить пользователя"},
		{Command: "ban", Description: "Забанить пользователя"},
		{Command: "unban", Description: "Разбанить пользователя"},
		{Command: "help", Description: "Справка"},
	}

	scopes := []struct {
		commands []telego.BotCommand
		scope    telego.BotCommandScope
	}{
		{privateCommands, tu.ScopeAllPrivateChats()},
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
