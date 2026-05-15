package bot

import (
	"context"
	"log/slog"
	"math/rand"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/shared"
)

// quipSender is the narrow telego surface the /roast and /praise
// handlers need. Not added to the shared GamesSender; the production
// rate-limited client satisfies this via SendMessage.
type quipSender interface {
	SendMessage(ctx context.Context, params *telego.SendMessageParams) (*telego.Message, error)
}

// roastTemplates and praiseTemplates are curated, SFW, playful (not
// cruel) IT-flavoured one-liners. The single "%s" is the target's
// display string (already HTML-escaped before formatting). 15 each.
var roastTemplates = []string{
	"%s коммитит прямо в main и ещё спрашивает, почему прод лежит.",
	"%s пишет тесты после релиза - и то, если напомнить дважды.",
	"%s называет это \"быстрым фиксом\", и так уже третий спринт.",
	"%s оставил TODO в 2021-м, оно до сих пор там.",
	"%s дебажит принтами и гордится этим.",
	"%s мержит без ревью, потому что \"там же одна строчка\".",
	"%s закрыл задачу, не открыв ни одного файла.",
	"%s объясняет легаси словами \"оно как-то работает, не трогай\".",
	"%s оценивает задачу в час, делает три дня - стабильно.",
	"%s игнорирует линтер, как будто тот не для него писан.",
	"%s катит в пятницу вечером и уходит в отпуск.",
	"%s называет копипасту из StackOverflow архитектурным решением.",
	"%s пушит с сообщением \"fix\" в сто двадцатый раз.",
	"%s чинит баг, ломая два соседних, и считает это прогрессом.",
	"%s обещал задокументировать - это было давно и неправда.",
}

var praiseTemplates = []string{
	"%s пишет код, который понятен даже спустя полгода.",
	"%s закрывает техдолг, пока остальные спорят о табах и пробелах.",
	"%s оставляет ревью, после которого реально хочется стать лучше.",
	"%s ловит баг ещё до того, как тот доедет до прода.",
	"%s объясняет сложное так, что понимает даже джун.",
	"%s покрывает тестами то, что другие \"и так проверили глазами\".",
	"%s коммитит маленькими и осмысленными шагами - мечта ревьюера.",
	"%s читает доку до того, как спросить, и это бесценно.",
	"%s рефакторит так, что diff меньше, а смысла больше.",
	"%s держит CI зелёным и нервы команды - крепкими.",
	"%s пишет сообщения коммитов, по которым видно историю проекта.",
	"%s не катит в пятницу - и за это ему отдельное спасибо.",
	"%s разбирает инцидент без поиска виноватых, только по делу.",
	"%s оставляет код чище, чем нашёл, каждый раз.",
	"%s превращает легаси в читаемое, не сломав ни одного флоу.",
}

// QuipHandler implements "/roast [@user]" and "/praise [@user]".
// Stateless: no DB. If a @username argument is present the quip is
// addressed to that raw handle (no membership resolution - kept
// stateless and Telegram has no member lookup anyway); otherwise the
// caller is the target. Randomness is injectable for deterministic
// tests (mirrors the quiz/8ball pattern).
type QuipHandler struct {
	bot quipSender
	log *slog.Logger

	// rand lets tests inject a deterministic source. nil -> fresh
	// time-seeded source per dispatch.
	rand *rand.Rand
}

func NewQuipHandler(bot quipSender, log *slog.Logger) *QuipHandler {
	if log == nil {
		log = slog.Default()
	}
	return &QuipHandler{bot: bot, log: log}
}

// HandleRoast handles "/roast" and "/roast @user".
func (h *QuipHandler) HandleRoast(_ *th.Context, msg telego.Message) error {
	return h.handle(msg, roastTemplates)
}

// HandlePraise handles "/praise" and "/praise @user".
func (h *QuipHandler) HandlePraise(_ *th.Context, msg telego.Message) error {
	return h.handle(msg, praiseTemplates)
}

func (h *QuipHandler) handle(msg telego.Message, templates []string) error {
	if msg.From == nil || msg.From.IsBot {
		// No caller to attribute or fall back to.
		return nil
	}

	target := resolveQuipTarget(msg)

	r := h.rand
	if r == nil {
		r = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	tmpl := templates[r.Intn(len(templates))]

	body := strings.Replace(tmpl, "%s", target, 1)
	_, err := h.bot.SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: msg.Chat.ID},
		Text:      body,
		ParseMode: telego.ModeHTML,
		ReplyParameters: &telego.ReplyParameters{
			MessageID: msg.MessageID,
		},
	})
	if err != nil {
		h.log.Warn("quip reply failed", "error", err, "chat_id", msg.Chat.ID)
	}
	return err
}

// resolveQuipTarget returns the HTML-escaped display string the quip is
// aimed at. Priority:
//  1. an explicit @handle in the command arguments (raw token, escaped),
//  2. otherwise the caller's @username / first name.
//
// The @handle is taken verbatim from user input, so it is escaped via
// shared.EscapeHTML before being placed into an HTML-parsed message; a
// crafted "argument" like "<b>" can never inject markup.
func resolveQuipTarget(msg telego.Message) string {
	arg := strings.TrimSpace(commandArgs(msg.Text))
	if arg != "" {
		// Take only the first whitespace token as the target; ignore
		// any trailing words so "/roast @bob and friends" still works.
		if i := strings.IndexFunc(arg, func(r rune) bool {
			return r == ' ' || r == '\t' || r == '\n'
		}); i >= 0 {
			arg = arg[:i]
		}
		handle := strings.TrimPrefix(arg, "@")
		if handle != "" {
			return "@" + shared.EscapeHTML(handle)
		}
	}
	display := shared.UserDisplay(msg.From.Username, msg.From.FirstName)
	if display == "" {
		// Last resort: a stable reference even with no username/name.
		return "участник"
	}
	return display
}
