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

// eightBallSender is the narrow telego surface the /8ball handler needs.
// Declared here (not added to the shared GamesSender) so tests can swap
// in a recording stub. The production rate-limited tgclient already
// satisfies this with its SendMessage method.
type eightBallSender interface {
	SendMessage(ctx context.Context, params *telego.SendMessageParams) (*telego.Message, error)
}

// eightBallAnswers is a curated, SFW, IT-flavoured set of magic-8-ball
// verdicts (affirmative / non-committal / negative, ~8-ball tradition).
// Kept neutral and workplace-safe: this runs in a 200-member chat.
// Append freely - replayability scales with the pool; the test asserts
// only a healthy lower bound, not an exact size.
var eightBallAnswers = []string{
	// --- affirmative ---
	"Бесспорно. Деплой пройдёт гладко.",
	"Да, но сначала прогони тесты.",
	"Однозначно да - даже линтер не против.",
	"Можешь на это положиться, как на LTS-версию.",
	"Скорее всего да, если не трогать прод в пятницу.",
	"Знаки указывают на да. Но добавь логов на всякий случай.",
	"Да. Спроси у сеньора, он подтвердит.",
	"Похоже на да - но это не точно, как оценка в стори-поинтах.",
	"Перспективы хорошие, ревьюер уже поставил approve.",
	"Да, как только закроешь техдолг.",
	"Да. CI зелёный, звёзды сошлись.",
	"Определённо да - оно даже воспроизводится стабильно.",
	"Да, и даже прод-инцидента не будет.",
	"Можно. Главное - сначала забэкапь.",
	"Да. Этот кейс уже покрыт тестом.",
	"Уверенно да - архитектор кивнул.",
	"Да, документация на твоей стороне.",
	"Без сомнений. Фича-флаг уже выкатан.",
	"Да. Даже QA не нашёл, к чему придраться.",
	"Так точно - всё идемпотентно, не страшно повторить.",
	"Да, и грейс-период это переживёт.",
	"Смело да. Откат всё равно в одну команду.",
	"Да - кэш сегодня на твоей стороне.",
	"Конечно. Это ровно то, для чего писали этот модуль.",
	"Да. Метрика уже поползла вверх.",
	// --- non-committal ---
	"Не уверен, перезапусти вопрос и попробуй ещё раз.",
	"Спроси позже - сейчас идёт миграция.",
	"Лучше не говорить - это уйдёт в постмортем.",
	"Сейчас не могу предсказать, CI ещё красный.",
	"Сконцентрируйся и спроси снова, когда соберёшь билд.",
	"Туманно. Зависит от того, какая ветка задеплоена.",
	"Спроси у того, кто писал этот код. Если найдёшь его.",
	"Это есть в трекере. Где-то. Под меткой 'позже'.",
	"50 на 50, как покрытие тестами в этом репозитории.",
	"Ответ в логах. Которые мы, конечно, не пишем.",
	"Зависит. Классический ответ архитектора.",
	"Вернись после ретро, обсудим.",
	"Неясно - кажется, это за фича-флагом.",
	"Подожди, прогрею кэш и отвечу.",
	"Сложно сказать, окружение flaky.",
	"Зависит от часового пояса того, кто катит.",
	"Спрошу у модели и вернусь... шучу, думай сам.",
	"Это серая зона спецификации. Удачи.",
	"Не сейчас - идёт ретрай с экспоненциальной задержкой.",
	"Расплывчато, как требования в этом тикете.",
	// --- negative ---
	"Даже не рассчитывай. Это легаси, его лучше не трогать.",
	"Мой ответ - нет. Откатывай.",
	"По моим данным - нет. И ещё ревью не пройдено.",
	"Перспективы так себе - похоже на флаки-тест.",
	"Очень сомнительно. Сначала почини флоу деплоя.",
	"Нет. И в пятницу - тем более нет.",
	"Нет. Это уже ломали, не повторяй.",
	"Абсолютно нет - прод этого не переживёт.",
	"Нет. Линтер против, и он прав.",
	"Не стоит. Этот путь ведёт в постмортем.",
	"Нет - на это нет ни тестов, ни смелости.",
	"Категорически нет. Бэкапа же опять нет.",
	"Нет. Зависимость уже deprecated.",
	"Лучше не надо - там гонка по данным.",
	"Нет. Это не баг, это 'фича', не трогай.",
	"Откажись. Сложность не стоит результата.",
	"Нет, оно только что упало в стейджинге.",
	"Не сегодня - мейнтейнер в отпуске.",
	"Нет. Это уйдёт в неоплачиваемый техдолг.",
	"Точно нет - rate limit тебя не пустит.",
}

// EightBallHandler wires "/8ball <вопрос>" to a random curated verdict.
// Stateless: no DB, no storage. Randomness is injectable so tests are
// deterministic (mirrors the quiz handler's rand field).
type EightBallHandler struct {
	bot eightBallSender
	log *slog.Logger

	// rand lets tests inject a deterministic source. nil falls back to
	// a fresh time-seeded *rand.Rand per dispatch (fine for the small
	// number of /8ball calls a chat produces).
	rand *rand.Rand
}

func NewEightBallHandler(bot eightBallSender, log *slog.Logger) *EightBallHandler {
	if log == nil {
		log = slog.Default()
	}
	return &EightBallHandler{bot: bot, log: log}
}

// HandleEightBall handles "/8ball <вопрос>". A non-empty question is
// required; an empty one gets a Russian usage hint (reply-to). The
// verdict is picked from eightBallAnswers using the injected or a
// time-seeded rand source.
func (h *EightBallHandler) HandleEightBall(_ *th.Context, msg telego.Message) error {
	if msg.From == nil || msg.From.IsBot {
		// No user to address; nothing to do.
		return nil
	}

	question := commandArgs(msg.Text)
	if question == "" {
		return h.reply(msg, eightBallUsage())
	}

	r := h.rand
	if r == nil {
		r = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	answer := eightBallAnswers[r.Intn(len(eightBallAnswers))]

	body := "\U0001F3B1 " + shared.EscapeHTML(answer)
	return h.reply(msg, body)
}

// eightBallUsage is the hint shown when the question is missing.
func eightBallUsage() string {
	return "Спросите шар о чём-нибудь: <code>/8ball Стоит ли катить в прод в пятницу?</code>"
}

// commandArgs returns everything after the first whitespace-delimited
// token (the command itself), trimmed. Returns "" when the message is
// just the bare command. Shared by the stateless quip/8ball handlers.
func commandArgs(text string) string {
	text = strings.TrimSpace(text)
	idx := strings.IndexFunc(text, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n'
	})
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(text[idx+1:])
}

func (h *EightBallHandler) reply(msg telego.Message, body string) error {
	_, err := h.bot.SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: msg.Chat.ID},
		Text:      body,
		ParseMode: telego.ModeHTML,
		ReplyParameters: &telego.ReplyParameters{
			MessageID: msg.MessageID,
		},
	})
	if err != nil {
		h.log.Warn("8ball reply failed", "error", err, "chat_id", msg.Chat.ID)
	}
	return err
}
