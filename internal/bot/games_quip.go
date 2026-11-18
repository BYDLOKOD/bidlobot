package bot

// Curated Russian reputation copy.
//
// The positive catalogue is the launch set from the target
// specification: lines 1-16 are generic praise, 17-24 help, 25-32
// explanation, 33-40 review. Lines 17-40 are used only when the sender
// explicitly selects the reason; the bot never infers a reason from the
// quoted message. A public line is followed by the deterministic signed
// receipt, so the copy never replaces the amount that changed.
//
// Selection avoids repeating a line among the last five public uses of
// the same family in the same chat where alternatives exist. The history
// is process-local and bounded; copy rotation does not need durable
// state.

import (
	"math/rand/v2"
	"strings"
	"sync"
)

type repReason int

const (
	repReasonNone repReason = iota
	repReasonHelp
	repReasonExplanation
	repReasonReview
)

func (r repReason) family() string {
	switch r {
	case repReasonHelp:
		return "praise-help"
	case repReasonExplanation:
		return "praise-explanation"
	case repReasonReview:
		return "praise-review"
	default:
		return "praise-generic"
	}
}

// parseRepReason splits an optional explicit reason from the command
// arguments. Only an unprefixed first word is a reason; "@alice" stays a
// target.
func parseRepReason(args string) (repReason, string) {
	args = strings.TrimSpace(args)
	if args == "" {
		return repReasonNone, ""
	}
	first, rest := args, ""
	if i := strings.IndexAny(args, " \t\n"); i >= 0 {
		first = args[:i]
		rest = strings.TrimSpace(args[i+1:])
	}
	switch strings.ToLower(first) {
	case "help", "помощь":
		return repReasonHelp, rest
	case "explanation", "explain", "объяснение":
		return repReasonExplanation, rest
	case "review", "ревью":
		return repReasonReview, rest
	default:
		return repReasonNone, args
	}
}

var praiseGeneric = []string{
	"Тут плюс к репе напрашивался.",
	"Этот плюс по адресу.",
	"За такое и репу не жалко.",
	"Вот и повод сказать спасибо.",
	"Хорошо, что чат заметил.",
	"За это хочется поставить плюс.",
	"Эту благодарность передали по адресу.",
	"Спасибо перевели в репу.",
	"Мелочь в балансе, но по делу.",
	"Этот плюс не за красивые логи.",
	"Плюс дошёл без повторной отправки.",
	"Перевод без комиссии банка.",
	"На этот раз хватило короткого \u00abспасибо\u00bb.",
	"Репа пришла. Письмо в бухгалтерию не нужно.",
	"У этой благодарности есть квитанция.",
	"Этот плюс можно пустить в дело.",
}

var praiseHelp = []string{
	"Подхватить чужую задачу в нужный момент \u2014 отдельный навык.",
	"Спасибо, что помогли довести дело до конца.",
	"С такой помощью можно возвращаться к работе.",
	"В нужную минуту нашлась пара свободных рук.",
	"Спасибо, что не пришлось разбираться в одиночку.",
	"Спасибо, что нашлось на это время.",
	"После такой подсказки снова понятно, за что браться.",
	"Хорошо, когда на просьбу о помощи отвечают делом.",
}

var praiseExplanation = []string{
	"Теперь понятно, где искать причину.",
	"Спасибо за ответ, который не пришлось расшифровывать.",
	"Эту мысль теперь можно объяснить следующему человеку.",
	"Из объяснения понятно, что проверить сначала.",
	"Спасибо, что показали ход мысли, а не только результат.",
	"Пример сработал лучше длинной инструкции.",
	"Теперь понятно, почему первый способ не подошёл.",
	"Можно закрыть лишние вкладки: ответ нашёлся здесь.",
}

var praiseReview = []string{
	"Хорошо, что это заметили до релиза.",
	"Спасибо за замечание, по которому понятно, что исправить.",
	"Спасибо, что внимательно прошлись по коду.",
	"Один вопрос в разборе сэкономил ещё один круг правок.",
	"Спасибо, что проверили и случай за пределами примера.",
	"Хорошо, что нашлась конкретная строка для правки.",
	"После такого разбора проще оставить код следующему человеку.",
	"Спасибо за конкретику: понятно, что оставить и что переделать.",
}

func praiseCatalogue(reason repReason) []string {
	switch reason {
	case repReasonHelp:
		return praiseHelp
	case repReasonExplanation:
		return praiseExplanation
	case repReasonReview:
		return praiseReview
	default:
		return praiseGeneric
	}
}

// repCopySelector keeps the last five public line indices per chat and
// family. Safe for concurrent use: slash handlers and the reaction
// batcher share one selector.
type repCopySelector struct {
	mu   sync.Mutex
	last map[repCopyFamily][]int
}

type repCopyFamily struct {
	chatID int64
	name   string
}

func newRepCopySelector() *repCopySelector {
	return &repCopySelector{last: make(map[repCopyFamily][]int)}
}

// pick returns a line, preferring one outside the last five uses of the
// family. When the pool has no alternative it picks any line.
func (s *repCopySelector) pick(chatID int64, family string, lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	key := repCopyFamily{chatID: chatID, name: family}
	s.mu.Lock()
	defer s.mu.Unlock()

	recent := s.last[key]
	recentSet := make(map[int]struct{}, len(recent))
	for _, idx := range recent {
		recentSet[idx] = struct{}{}
	}
	candidates := make([]int, 0, len(lines))
	for i := range lines {
		if _, used := recentSet[i]; !used {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 0 {
		candidates = make([]int, len(lines))
		for i := range lines {
			candidates[i] = i
		}
	}
	idx := candidates[rand.IntN(len(candidates))]

	recent = append(recent, idx)
	if len(recent) > 5 {
		recent = recent[len(recent)-5:]
	}
	s.last[key] = recent
	return lines[idx]
}

func (s *repCopySelector) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = make(map[repCopyFamily][]int)
}

// repCopy is the process-wide selector shared by the slash handler and
// the reaction report, so a line is not repeated between the two inputs.
var repCopy = newRepCopySelector()

func praiseLine(chatID int64, reason repReason) string {
	return repCopy.pick(chatID, reason.family(), praiseCatalogue(reason))
}
