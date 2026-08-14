package bot

// Full command reference in Telegram MarkdownV2.
//
// /help in a supergroup sends this to the caller's private chat with the
// bot (not the group), so a long list never spams the chat. The same
// builder backs the DM /help and /start replies.
//
// MarkdownV2 escaping: every char in `_ * [ ] ( ) ~ \` > # + - = | { } . !`
// outside a code span must be escaped or Telegram answers 400
// "can't parse entities". Commands live inside `code` spans (no escaping
// needed there); descriptions go through mdV2Escape so a stray `.` or `!`
// can never break the whole message.

import "strings"

// helpEntry is one command line: the full invocation example plus a short
// description of what it does.
type helpEntry struct {
	cmd  string // e.g. "/battle Go Rust"
	desc string // e.g. "голосование реакциями, 60 секунд"
}

// helpSection groups entries under a bold MarkdownV2 header.
type helpSection struct {
	title   string
	entries []helpEntry
}

// helpSections is the ordered command reference. Sources: setup.go menus,
// registerRoutes and registerGameRoutes wiring.
var helpSections = []helpSection{
	{
		title: "Статистика и инструменты",
		entries: []helpEntry{
			{cmd: "/stats", desc: "обзор чата"},
			{cmd: "/stats top", desc: "топ участников"},
			{cmd: "/stats today", desc: "активность за день"},
			{cmd: "/summarize 50", desc: "итог последних 50 сообщений через AI (админы), алиас /итог 50"},
			{cmd: "/flush", desc: "повторить неудачные экспорты и саммари"},
			{cmd: "/help", desc: "полный список команд (в личку)"},
		},
	},
	{
		title: "Игры",
		entries: []helpEntry{
			{cmd: "/dice", desc: "бросок кубика"},
			{cmd: "/battle Go Rust", desc: "голосование реакциями за 60 секунд"},
			{cmd: "/quiz", desc: "угадай язык по сниппету"},
			{cmd: "/poll вопрос | вариант1 | вариант2", desc: "опрос"},
			{cmd: "/8ball вопрос", desc: "шар предсказаний"},
			{cmd: "/guess", desc: "угадай число от 1 до 100"},
			{cmd: "/hangman", desc: "виселица на IT-словах"},
			{cmd: "/duel @user", desc: "дуэль"},
			{cmd: "/trivia", desc: "IT-викторина"},
		},
	},
	{
		title: "Репутация",
		entries: []helpEntry{
			{cmd: "/praise @user", desc: "похвалить участника"},
			{cmd: "/roast @user", desc: "поджарить участника"},
			{cmd: "/rep", desc: "твой баланс репутации"},
			{cmd: "/reptop", desc: "топ репутации"},
		},
	},
	{
		title: "Реферальные ссылки",
		entries: []helpEntry{
			{cmd: "/refs", desc: "все рефки чата"},
			{cmd: "/refreg", desc: "добавить рефку"},
		},
	},
	{
		title: "Админы",
		entries: []helpEntry{
			{cmd: "/refreport ID", desc: "удалить рефку по ID"},
		},
	},
}

// ownerHelpEntries are appended only for the bot owner.
var ownerHelpEntries = []helpEntry{
	{cmd: "/chats", desc: "список чатов, где работает бот"},
}

// mdV2Escape escapes every MarkdownV2 special character in plain text.
// Used on descriptions; commands are wrapped in code spans instead.
func mdV2Escape(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch r {
		case '_', '*', '[', ']', '(', ')', '~', '`', '>', '#', '+', '-', '=', '|', '{', '}', '.', '!':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// buildHelpMarkdown renders the full command reference in MarkdownV2.
// owner appends the owner-only block.
func buildHelpMarkdown(owner bool) string {
	var b strings.Builder
	b.WriteString("*" + mdV2Escape("BidloBot - команды") + "*\n")

	for _, sec := range helpSections {
		if len(sec.entries) == 0 {
			continue
		}
		b.WriteString("\n*" + mdV2Escape(sec.title) + "*\n")
		for _, e := range sec.entries {
			b.WriteString("`" + e.cmd + "` \\- " + mdV2Escape(e.desc) + "\n")
		}
	}
	if owner && len(ownerHelpEntries) > 0 {
		b.WriteString("\n*Владелец*\n")
		for _, e := range ownerHelpEntries {
			b.WriteString("`" + e.cmd + "` \\- " + mdV2Escape(e.desc) + "\n")
		}
	}
	return b.String()
}
