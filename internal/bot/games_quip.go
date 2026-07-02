package bot

import (
	"context"
	"html"
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
// display string (already HTML-escaped before formatting). Append
// freely - the test asserts only a healthy lower bound, not an exact
// size; replayability scales with the pool.
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
	"%s закрывает тикет комментарием \"у меня работает\".",
	"%s держит ветку feature/wip уже восьмой месяц.",
	"%s решает любую проблему добавлением ещё одного флага конфигурации.",
	"%s тестирует сразу на проде - смелость на грани отчаянности.",
	"%s рефакторит весь модуль за день до релиза.",
	"%s называет 500 строк в одном методе \"пока временно\".",
	"%s отвечает \"щас гляну\" и пропадает на три дня.",
	"%s правит прод хотфиксом прямо в редакторе по ssh.",
	"%s игнорит алерты, пока их не станет ровно сто.",
	"%s оценивает в стори-поинтах по фазе луны.",
	"%s катит миграцию без отката и крестится.",
	"%s называет глобальную переменную \"tmp\" и оставляет навсегда.",
	"%s пишет коммит \"asdf\" и считает это документацией.",
	"%s закрывает баг как \"не воспроизводится\" не пытаясь воспроизвести.",
	"%s сначала пишет код, потом придумывает, что он делает.",
	"%s добавляет sleep(5), чтобы починить гонку, и празднует.",
	"%s держит 47 вкладок со StackOverflow как систему документации.",
	"%s комментирует код фразой \"// не трогать, работает\".",
	"%s выкатывает на всех сразу - канареечный релиз для слабаков.",
	"%s называет отсутствие тестов \"доверием к команде\".",
	"%s отвечает на код-ревью \"исправлю потом\" и не исправляет.",
	"%s решает merge-конфликт, оставляя обе версии на всякий случай.",
	"%s узнаёт об инциденте из чата, а не из мониторинга.",
	"%s хранит секреты в коде, потому что \"это же приватный репозиторий\".",
	"%s оптимизирует то, что выполняется раз в год, и игнорит горячий путь.",
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
	"%s пишет такой понятный PR, что ревью занимает пять минут.",
	"%s добавляет логи ровно там, где они однажды спасут дебаг.",
	"%s задаёт на груминге вопрос, который экономит неделю работы.",
	"%s умеет сказать \"я не знаю\" и тут же пойти разобраться.",
	"%s пишет тесты, по которым понятно, как работает код.",
	"%s сначала измеряет, потом оптимизирует - и всегда в этом порядке.",
	"%s оставляет в коде комментарий \"почему\", а не \"что\".",
	"%s доводит инцидент до честного постмортема без драмы.",
	"%s ревьюит чужой код внимательнее, чем свой.",
	"%s делает сложную фичу скучно надёжной - высший пилотаж.",
	"%s удаляет больше кода, чем добавляет, и система только крепнет.",
	"%s обновляет доку в том же PR - легенды существуют.",
	"%s спокойно откатывает релиз и не делает из этого трагедии.",
	"%s пишет идемпотентно, потому что знает: повторят всё.",
	"%s превращает флаки-тест в стабильный, а не в skip.",
	"%s наставляет джунов так, что они растут на глазах.",
	"%s продумывает крайние случаи раньше, чем о них спросят.",
	"%s держит обещания по срокам, потому что честно их оценивает.",
	"%s закрывает алерты разбором причины, а не отключением.",
	"%s пишет migration с откатом и проверяет его заранее.",
	"%s умеет вовремя сказать \"это переусложнено\" - и упростить.",
	"%s оставляет систему в состоянии, в котором её не страшно дежурить.",
	"%s документирует решение так, что через год спасибо скажет он сам.",
	"%s не геройствует ночью, а чинит процесс, чтобы ночей не было.",
	"%s делает ревью добрым по тону и жёстким по сути.",
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
//  1. an explicit handle in the command arguments (raw token, escaped),
//  2. otherwise the caller's handle / first name.
//
// The handle is rendered WITHOUT a leading '@': a literal "@handle"
// makes Telegram notify that account, so "/roast @victim" would ping
// the victim on every invocation. Bare text is inert. The token is
// taken verbatim from user input, so it is escaped via
// html.EscapeString before being placed into an HTML-parsed message; a
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
			return html.EscapeString(handle)
		}
	}
	display := shared.UserDisplay(msg.From.Username, msg.From.FirstName)
	if display == "" {
		// Last resort: a stable reference even with no username/name.
		return "участник"
	}
	return display
}
