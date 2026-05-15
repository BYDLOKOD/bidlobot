package bot

import (
	"strings"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/games/dice"
)

// GamesInlineRouter is the InlineGameRouter implementation that owns
// inline-mode dispatch for the three mini-games (dice, battle, quiz).
// All game inline results are pure suggestions to send a slash command;
// the slash handlers do the actual work. This keeps the per-game
// callback flow unified (no special-casing inline vs. slash entry).
type GamesInlineRouter struct{}

// NewGamesInlineRouter constructs a router with no per-game state -
// inline routing is fully derived from the query text.
func NewGamesInlineRouter() *GamesInlineRouter { return &GamesInlineRouter{} }

// Route dispatches "dice", "battle", and "quiz" inline commands. Other
// commands fall through (handled=false) so the InlineService picks them
// up via its default switch.
func (r *GamesInlineRouter) Route(cmd string, args []string, _ telego.User) ([]telego.InlineQueryResult, bool) {
	switch cmd {
	case "dice":
		return r.dice(args), true
	case "battle":
		return r.battle(args), true
	case "quiz":
		return r.quiz(args), true
	case "poll":
		return r.passthrough("poll", "📊 Опрос", "Вопрос | вар1 | вар2 (или: poll quiz Вопрос | *Верный | ...)", args), true
	case "8ball":
		return r.passthrough("8ball", "🎱 Шар предсказаний", "Задайте вопрос: 8ball катить в прод?", args), true
	case "roast":
		return r.passthrough("roast", "🔥 Поджарить", "/roast [@user]", args), true
	case "praise":
		return r.passthrough("praise", "👏 Похвалить", "/praise [@user]", args), true
	case "guess":
		return r.passthrough("guess", "🔢 Угадай число", "/guess - старт, /guess N - попытка, /guess top", args), true
	case "hangman":
		return r.passthrough("hangman", "🪢 Виселица", "/hangman - старт, /hangman <буква>", args), true
	case "duel":
		return r.passthrough("duel", "⚔️ Дуэль", "/duel @user - кто кого", args), true
	case "trivia":
		return r.passthrough("trivia", "🎓 IT-викторина", "/trivia - вопрос, /trivia top", args), true
	}
	return nil, false
}

// passthrough builds a single pure slash-command suggestion (mirrors the
// dice/quiz pattern): with no args it shows the usage hint, otherwise it
// forwards the typed args to the slash handler, which does the work and
// owns all validation. Keeps one code path for inline and slash entry.
func (r *GamesInlineRouter) passthrough(cmd, title, hint string, args []string) []telego.InlineQueryResult {
	send := "/" + cmd
	desc := hint
	if len(args) > 0 {
		send += " " + strings.Join(args, " ")
		desc = "Отправить " + send
	}
	return toResults([]inlineCommand{{
		id:          "games_" + cmd + "_" + sha1Hex(send),
		title:       title,
		description: desc,
		send:        send,
	}})
}

// dice surfaces a single result that, when chosen, sends "/dice" or
// "/dice <emoji>" into the chat. The slash handler then performs the
// actual SendDice call. Routing through the slash command keeps a
// single code path for both entries.
func (r *GamesInlineRouter) dice(args []string) []telego.InlineQueryResult {
	if len(args) == 0 {
		return toResults([]inlineCommand{{
			id:          "games_dice_default",
			title:       "🎲 Бросить кубик",
			description: "Отправить /dice (1-6)",
			send:        "/dice",
		}})
	}
	emoji := strings.TrimSpace(args[0])
	if !dice.IsAllowedEmoji(emoji) {
		return toResults([]inlineCommand{{
			id:          "games_dice_hint",
			title:       "🎲 Поддерживаемые: " + strings.Join(dice.AllowedEmojis, " "),
			description: "Введите один из шести dice-смайлов или оставьте пустым",
			send:        "/dice",
		}})
	}
	send := "/dice " + emoji
	return toResults([]inlineCommand{{
		id:          "games_dice_" + sha1Hex(send),
		title:       emoji + " Бросить",
		description: "Отправить " + send,
		send:        send,
	}})
}

// battle surfaces the slash launcher. With insufficient args we show a
// hint; with both sides we propose "/battle X Y" - the slash handler
// posts the two reaction targets and starts the 60s timer.
func (r *GamesInlineRouter) battle(args []string) []telego.InlineQueryResult {
	if len(args) < 2 {
		return toResults([]inlineCommand{{
			id:          "games_battle_hint",
			title:       "🥊 battle X Y",
			description: "Укажите две стороны (одно слово или эмодзи каждая)",
			send:        "/help",
		}})
	}
	left := strings.TrimSpace(args[0])
	right := strings.TrimSpace(args[1])
	if left == "" || right == "" {
		return toResults([]inlineCommand{{
			id:          "games_battle_empty",
			title:       "🥊 battle X Y",
			description: "Стороны не должны быть пустыми",
			send:        "/help",
		}})
	}
	send := "/battle " + left + " " + right
	return toResults([]inlineCommand{{
		id:          "games_battle_" + sha1Hex(send),
		title:       "🥊 " + left + " vs " + right,
		description: "Отправить " + send,
		send:        send,
	}})
}

// quiz routes to the /quiz launcher. "quiz top" forwards to the
// leaderboard variant.
func (r *GamesInlineRouter) quiz(args []string) []telego.InlineQueryResult {
	if len(args) > 0 && strings.EqualFold(args[0], "top") {
		return toResults([]inlineCommand{{
			id:          "games_quiz_top",
			title:       "🧩 /quiz top",
			description: "Топ-5 угадавших в этом чате",
			send:        "/quiz top",
		}})
	}
	return toResults([]inlineCommand{{
		id:          "games_quiz",
		title:       "🧩 Код-квиз",
		description: "Отправить /quiz - угадай язык по коду",
		send:        "/quiz",
	}})
}
