// Package hangman implements the /hangman game: the bot picks a curated
// IT/programming word per chat, members guess one letter at a time, and
// the round ends on a full reveal (win) or MaxWrong wrong letters (loss).
//
// Words and randomness are injectable so tests are deterministic. The
// word list is baked into the binary (no runtime I/O) and SFW.
package hangman

import (
	"math/rand"
	"strings"
	"time"
)

// MaxWrong is the classic hangman wrong-guess budget. The seventh wrong
// letter ends the round as a loss.
const MaxWrong = 6

// words is the curated pool: Russian and English IT terms - languages,
// tools, concepts. Stored uppercased-on-use; keep entries lowercase here
// for readability. Minimum length kept >=4 so a single letter does not
// trivially reveal the whole word.
var words = []string{
	// Languages
	"python", "golang", "rust", "clojure", "typescript", "kotlin",
	"haskell", "scala", "elixir", "erlang",
	// Concepts
	"горутина", "компилятор", "рефактор", "легаси", "замыкание",
	"рекурсия", "интерфейс", "указатель", "массив", "очередь",
	"кэширование", "конкурентность", "дедлок", "мьютекс", "хэштаблица",
	"полиморфизм", "наследование", "абстракция", "инкапсуляция",
	// Tools / infra
	"kubernetes", "docker", "terraform", "ansible", "prometheus",
	"grafana", "postgres", "redis", "nginx", "kafka",
	"гитхаб", "линукс", "контейнер", "пайплайн", "деплой",
	// More languages / runtimes
	"javascript", "swift", "ruby", "perl", "carbon", "ocaml",
	"fortran", "cobol", "groovy", "dart", "julia", "crystal",
	"assembler", "pascal", "prolog", "scheme", "racket",
	// More concepts
	"алгоритм", "структура", "сложность", "итератор", "генератор",
	"каррирование", "корутина", "семафор", "монада", "функтор",
	"замер", "профилировка", "трассировка", "телеметрия", "идемпотентность",
	"сериализация", "валидация", "хеширование", "шифрование", "компрессия",
	"паттерн", "рефакторинг", "тестирование", "отладка", "оптимизация",
	"наблюдаемость", "масштабирование", "репликация", "шардирование", "консенсус",
	"транзакция", "блокировка", "конвейер", "стек", "дерево",
	"граф", "куча", "список", "множество", "словарь",
	// Web / network / db
	"протокол", "сокет", "маршрут", "запрос", "ответ",
	"заголовок", "куки", "сессия", "токен", "вебхук",
	"латентность", "индекс", "миграция", "репозиторий", "консистентность",
	"кластер", "балансировщик", "прокси", "шлюз", "брокер",
	// Tools / infra / security
	"compiler", "linker", "debugger", "profiler", "sandbox",
	"firewall", "certificate", "namespace", "scheduler", "garbage",
	"webpack", "babel", "vite", "gradle", "maven",
	"jenkins", "argocd", "vault", "consul", "envoy",
	"sqlite", "mongodb", "rabbitmq", "elastic", "clickhouse",
	"монолит", "микросервис", "оркестратор", "артефакт", "реестр",
	"уязвимость", "аутентификация", "авторизация", "песочница", "аудит",
}

// WordCount returns the size of the pool. Used by tests to assert the
// list is healthy without hardcoding the number in multiple places.
func WordCount() int { return len(words) }

// PickWord returns a random word from the pool, uppercased so display
// and guess comparison are case-insensitive without per-call folding.
// A nil rnd falls back to a fresh time-seeded source.
func PickWord(rnd *rand.Rand) string {
	if rnd == nil {
		rnd = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	return strings.ToUpper(words[rnd.Intn(len(words))])
}

// IsSingleLetter reports whether s is exactly one letter (Latin or
// Cyrillic). Multi-character or non-letter input is rejected by the
// service with a friendly hint rather than counted as a wrong guess.
func IsSingleLetter(s string) bool {
	r := []rune(s)
	if len(r) != 1 {
		return false
	}
	c := r[0]
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		return true
	case c >= 'а' && c <= 'я', c >= 'А' && c <= 'Я', c == 'ё', c == 'Ё':
		return true
	default:
		return false
	}
}

// NormalizeLetter uppercases a single-letter guess so it matches the
// uppercased secret. Cyrillic ё/Ё is normalized to itself (not folded
// into е) so the word list can use either explicitly.
func NormalizeLetter(s string) string {
	return strings.ToUpper(s)
}
