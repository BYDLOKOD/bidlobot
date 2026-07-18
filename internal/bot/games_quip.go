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

// roastTemplates and praiseTemplates are the shared, deliberately
// understated corpora used by both the stateless /roast and /praise
// quips and the durable ReputationHandler responses. Every entry
// carries exactly one "%s" placeholder (the target's display string,
// already HTML-escaped before formatting); every entry closes with an
// unfinished thought marked by "..." to keep one quiet voice. The
// TestQuipTemplatesCuratedCounts guard enforces both invariants.
var roastTemplates = []string{
	"ну ты даёшь, %s...",
	"%s опять отличился... не в ту сторону.",
	"%s сделал как проще... всем остальным теперь сложнее.",
	"%s починил одно... остальное стало понятнее, но не лучше.",
	"%s снова назвал это быстрым фиксом...",
	"%s проверил на проде... другого стенда, видимо, не заслужили.",
	"%s оставил всё как есть... только хуже.",
	"%s добавил ещё один слой... до истины теперь не докопаться.",
	"%s забыл про тесты... тесты не забыли.",
	"%s закрыл задачу... проблема осталась.",
	"%s выбрал временное решение... навсегда.",
	"%s снова коммитит в main... тишина перед пайплайном.",
	"%s написал TODO... работа почти завершена.",
	"%s оптимизировал... теперь медленно, но сложно.",
	"%s объяснил архитектуру... вопросов стало больше.",
	"%s убрал проверку... ошибка тоже исчезла из логов.",
	"%s не прочитал сообщение выше... традиции важны.",
	"%s нашёл крайний случай... уже после релиза.",
	"%s добавил ретрай... причина осталась.",
	"%s переименовал баг... теперь это ограничение.",
	"%s снова забыл контекст... контекст не сопротивлялся.",
	"%s оставил магическое число... пусть следующие гадают.",
	"%s сделал рефакторинг... функциональность вспоминают.",
	"%s сократил код... вместе с нужной веткой.",
	"%s обновил зависимость... день перестал быть свободным.",
	"%s написал комментарий... код всё равно не согласен.",
	"%s вынес абстракцию... пользоваться стало некому.",
	"%s решил не логировать... неизвестность спокойнее.",
	"%s пропустил ошибку... она не пропустила прод.",
	"%s добавил флаг... выключить последствия нельзя.",
	"%s сделал красиво... работать было необязательно.",
	"%s снова не воспроизвёл баг... пользователь смог.",
	"%s выбрал дефолт... никто не знает какой.",
	"%s оставил миграцию на потом... данные уже ушли.",
	"%s проверил happy path... остальные пути грустят.",
	"%s добавил кеш... теперь ошибка быстрее.",
	"%s сделал универсально... конкретно не работает.",
	"%s убрал дублирование... вместе с различиями.",
	"%s всё задокументировал... кроме правды.",
	"%s закончил спринт... спринт с ним не закончил.",
}

var praiseTemplates = []string{
	"%s сделал хорошее дело... бывает.",
	"%s всё-таки помог... странный день.",
	"%s сегодня не подвёл... пока.",
	"%s закрыл задачу... тишина стала чуть терпимее.",
	"%s починил то, что давно просили... уже не верилось.",
	"%s оказался полезен... неловко вышло.",
	"%s сделал как надо... без лишнего шума.",
	"%s не прошёл мимо... зря мы сомневались.",
	"%s разобрался... кому-то же пришлось.",
	"%s спас чужой вечер... свой, видимо, уже нет.",
	"%s довёл дело до конца... редкое зрелище.",
	"%s оставил после себя рабочий код... почти надежда.",
	"%s заметил проблему раньше прода... невероятно.",
	"%s помог команде... и ничего не попросил.",
	"%s исправил баг... один из многих.",
	"%s сделал ревью... стало немного меньше страшно.",
	"%s объяснил спокойно... силы ещё остались.",
	"%s не усложнил... уже достижение.",
	"%s выбрал нормальное решение... случайность исключать нельзя.",
	"%s вспомнил про тесты... до релиза.",
	"%s удержал прод... ещё на один день.",
	"%s убрал техдолг... маленький кусок бесконечности.",
	"%s ответил по делу... чат даже притих.",
	"%s признал ошибку... мир не закончился.",
	"%s помог новичку... круг замкнулся не сразу.",
	"%s написал документацию... кому-то станет чуть легче.",
	"%s нашёл причину... не только симптом.",
	"%s не стал спорить и просто сделал... мрачно эффективно.",
	"%s вернул всё в рабочее состояние... ненадолго, наверное.",
	"%s проверил крайний случай... тот самый.",
	"%s сохранил чужое время... своего не пожалел.",
	"%s сделал понятнее... редкая роскошь.",
	"%s предупредил заранее... катастрофа отложена.",
	"%s пришёл с решением... без презентации на сорок слайдов.",
	"%s выдержал этот спринт... и даже помог другим.",
	"%s не бросил задачу на полпути... подозрительно.",
	"%s сделал меньше кода... и он работает.",
	"%s оставил систему лучше, чем нашёл... чуть-чуть.",
	"%s всё проверил... теперь можно бояться предметно.",
	"%s заслужил плюс... хотя радоваться рано.",
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
