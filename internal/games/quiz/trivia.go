package quiz

import (
	"errors"
	"math/rand"
	"time"
)

// Trivia is one multiple-choice IT general-knowledge question. It is a
// parallel data set to Snippet (code-language guessing) and shares none
// of its state: the existing /quiz flow is untouched. Question/Options
// are baked into the binary - no runtime I/O.
//
// Options always holds exactly 4 choices. CorrectIdx is the index into
// Options of the right answer in its CANONICAL order; the handler
// shuffles a copy and recomputes the displayed correct index, so editing
// this table never invalidates in-flight quizzes (each /trivia embeds
// its own shuffled order in callback_data).
type Trivia struct {
	Question   string
	Options    [4]string
	CorrectIdx int
}

// triviaSet is the curated pool of ~25 Russian IT-trivia questions
// (programming, sysadmin, history). Add new entries at the END so the
// stable indices used by callback_data do not shift for in-flight
// quizzes. SFW, factual.
var triviaSet = []Trivia{
	{
		Question:   "Что означает аббревиатура HTTP?",
		Options:    [4]string{"HyperText Transfer Protocol", "High Transfer Text Protocol", "HyperText Transmission Process", "Host Transfer Type Protocol"},
		CorrectIdx: 0,
	},
	{
		Question:   "Какой язык создал Гвидо ван Россум?",
		Options:    [4]string{"Ruby", "Python", "Perl", "PHP"},
		CorrectIdx: 1,
	},
	{
		Question:   "Сколько бит в одном байте?",
		Options:    [4]string{"4", "8", "16", "32"},
		CorrectIdx: 1,
	},
	{
		Question:   "Какая структура данных работает по принципу LIFO?",
		Options:    [4]string{"Очередь", "Стек", "Связный список", "Дерево"},
		CorrectIdx: 1,
	},
	{
		Question:   "Что такое SQL-инъекция?",
		Options:    [4]string{"Ускорение запросов", "Атака через неэкранированный ввод в запрос", "Метод репликации БД", "Тип индекса"},
		CorrectIdx: 1,
	},
	{
		Question:   "Какой порт по умолчанию у HTTPS?",
		Options:    [4]string{"80", "21", "443", "22"},
		CorrectIdx: 2,
	},
	{
		Question:   "Что делает команда git rebase?",
		Options:    [4]string{"Удаляет ветку", "Переносит коммиты на новую базу", "Клонирует репозиторий", "Откатывает рабочую копию"},
		CorrectIdx: 1,
	},
	{
		Question:   "Какая сложность бинарного поиска в отсортированном массиве?",
		Options:    [4]string{"O(n)", "O(log n)", "O(n log n)", "O(1)"},
		CorrectIdx: 1,
	},
	{
		Question:   "Кто считается автором языка C?",
		Options:    [4]string{"Деннис Ритчи", "Бьёрн Страуструп", "Джеймс Гослинг", "Кен Томпсон"},
		CorrectIdx: 0,
	},
	{
		Question:   "Что хранит DNS-запись типа A?",
		Options:    [4]string{"Почтовый сервер", "IPv4-адрес домена", "IPv6-адрес домена", "Псевдоним домена"},
		CorrectIdx: 1,
	},
	{
		Question:   "Какой принцип ООП скрывает внутреннее состояние объекта?",
		Options:    [4]string{"Наследование", "Полиморфизм", "Инкапсуляция", "Композиция"},
		CorrectIdx: 2,
	},
	{
		Question:   "Что такое идемпотентный HTTP-метод?",
		Options:    [4]string{"Всегда меняет состояние", "Повтор даёт тот же результат", "Требует авторизации", "Не возвращает тело"},
		CorrectIdx: 1,
	},
	{
		Question:   "Какая система контроля версий распределённая?",
		Options:    [4]string{"SVN", "CVS", "Git", "Perforce"},
		CorrectIdx: 2,
	},
	{
		Question:   "Что означает аббревиатура API?",
		Options:    [4]string{"Application Programming Interface", "Applied Process Integration", "Automated Program Index", "Abstract Protocol Identifier"},
		CorrectIdx: 0,
	},
	{
		Question:   "Какой статус-код HTTP означает 'Not Found'?",
		Options:    [4]string{"403", "500", "404", "301"},
		CorrectIdx: 2,
	},
	{
		Question:   "Что делает оператор SQL JOIN?",
		Options:    [4]string{"Удаляет строки", "Объединяет строки из таблиц по условию", "Создаёт индекс", "Блокирует таблицу"},
		CorrectIdx: 1,
	},
	{
		Question:   "Какая команда Linux показывает использование диска?",
		Options:    [4]string{"ps", "df", "top", "ls"},
		CorrectIdx: 1,
	},
	{
		Question:   "Что такое deadlock?",
		Options:    [4]string{"Утечка памяти", "Взаимная блокировка потоков", "Переполнение стека", "Гонка данных без блокировок"},
		CorrectIdx: 1,
	},
	{
		Question:   "В каком году вышел первый релиз языка Go?",
		Options:    [4]string{"2007", "2009", "2012", "2014"},
		CorrectIdx: 1,
	},
	{
		Question:   "Что хранит файл /etc/passwd в Linux?",
		Options:    [4]string{"Пароли в открытом виде", "Учётные записи пользователей", "Сетевые настройки", "Историю команд"},
		CorrectIdx: 1,
	},
	{
		Question:   "Какой формат данных использует JSON в основе?",
		Options:    [4]string{"XML-теги", "Пары ключ-значение", "Бинарные блоки", "CSV-строки"},
		CorrectIdx: 1,
	},
	{
		Question:   "Что такое REST?",
		Options:    [4]string{"Язык запросов к БД", "Архитектурный стиль для веб-API", "Протокол шифрования", "Формат сериализации"},
		CorrectIdx: 1,
	},
	{
		Question:   "Какой алгоритм сортировки имеет среднюю сложность O(n log n)?",
		Options:    [4]string{"Пузырьковая", "Быстрая (quicksort)", "Сортировка выбором", "Сортировка вставками"},
		CorrectIdx: 1,
	},
	{
		Question:   "Что делает команда docker build?",
		Options:    [4]string{"Запускает контейнер", "Собирает образ из Dockerfile", "Удаляет образ", "Публикует образ в реестр"},
		CorrectIdx: 1,
	},
	{
		Question:   "Какой протокол транспортного уровня без установки соединения?",
		Options:    [4]string{"TCP", "UDP", "TLS", "ICMP"},
		CorrectIdx: 1,
	},
	{
		Question:   "Что означает 'CI' в CI/CD?",
		Options:    [4]string{"Code Inspection", "Continuous Integration", "Container Image", "Cluster Init"},
		CorrectIdx: 1,
	},
}

// ErrTriviaIndex is returned when GetTrivia receives an out-of-range
// index (e.g. a stale callback_data after questions were removed).
var ErrTriviaIndex = errors.New("quiz: trivia index out of range")

// TriviaCount returns the number of available trivia questions.
func TriviaCount() int { return len(triviaSet) }

// GetTrivia returns the question at idx. Out-of-range -> ErrTriviaIndex.
func GetTrivia(idx int) (Trivia, error) {
	if idx < 0 || idx >= len(triviaSet) {
		return Trivia{}, ErrTriviaIndex
	}
	return triviaSet[idx], nil
}

// PickRandomTrivia returns a random question index. A nil rnd falls back
// to a fresh time-seeded source (mirrors PickRandom for snippets).
func PickRandomTrivia(r *rand.Rand) int {
	if r == nil {
		r = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	return r.Intn(len(triviaSet))
}

// BuildTriviaOptions returns the four answer labels for a question in
// randomised order together with the index (0..3) of the correct one in
// that shuffled order. The original CorrectIdx in the table is never
// exposed to the keyboard, so reordering choices on screen cannot leak
// the answer. Returns ErrTriviaIndex for a bad index.
func BuildTriviaOptions(triviaIdx int, r *rand.Rand) (labels []string, correctIdx int, err error) {
	tr, err := GetTrivia(triviaIdx)
	if err != nil {
		return nil, 0, err
	}
	if r == nil {
		r = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	type opt struct {
		text    string
		correct bool
	}
	opts := make([]opt, 0, 4)
	for i, o := range tr.Options {
		opts = append(opts, opt{text: o, correct: i == tr.CorrectIdx})
	}
	r.Shuffle(len(opts), func(i, j int) { opts[i], opts[j] = opts[j], opts[i] })
	labels = make([]string, 0, 4)
	correctIdx = -1
	for i, o := range opts {
		labels = append(labels, o.text)
		if o.correct {
			correctIdx = i
		}
	}
	return labels, correctIdx, nil
}
