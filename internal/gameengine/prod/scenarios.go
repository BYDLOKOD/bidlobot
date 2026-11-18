package prod

// scenario is an authored incident. A deep snapshot of the selected scenario
// is stored inside each incident, so deploying a new build cannot change the
// rules of a lobby or round that is already live.
type scenario struct {
	ID        string   `json:"id"`
	Version   int      `json:"version"`
	Title     string   `json:"title"`
	Premise   string   `json:"premise"`
	Debrief   string   `json:"debrief"`
	RestoreAt int      `json:"restore_at"`
	FinalOK   []string `json:"final_ok"`
	Turns     []turn   `json:"turns"`
}

type turn struct {
	Prompt  string   `json:"prompt"`
	Clue    string   `json:"clue"`
	Options []option `json:"options"`
}

type option struct {
	ID           string   `json:"id"`
	Label        string   `json:"label"`
	Conservative bool     `json:"conservative"`
	Requires     []string `json:"requires,omitempty"`
	GoodDelta    int      `json:"good_delta"`
	GoodEffect   string   `json:"good_effect"`
	BadDelta     int      `json:"bad_delta"`
	BadEffect    string   `json:"bad_effect"`
}

// effect returns the authored outcome for one option given the options applied
// in earlier turns. It is the deterministic outcome matrix of the scenario.
func (o option) effect(applied []string) (int, string) {
	if len(o.Requires) == 0 {
		return o.GoodDelta, o.GoodEffect
	}
	for _, need := range o.Requires {
		if !contains(applied, need) {
			return o.BadDelta, o.BadEffect
		}
	}
	return o.GoodDelta, o.GoodEffect
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// scenarioVersion is the authored rules version of every scenario below.
const scenarioVersion = 1

// scenarioIDs is the stable rotation order used when a sponsor opens a lobby
// without naming a scenario.
var scenarioIDs = []string{"search", "queue", "certs"}

func scenarioByID(id string) (scenario, bool) {
	switch id {
	case "search":
		return searchScenario(), true
	case "queue":
		return queueScenario(), true
	case "certs":
		return certsScenario(), true
	default:
		return scenario{}, false
	}
}

func searchScenario() scenario {
	return scenario{
		ID:        "search",
		Version:   scenarioVersion,
		Title:     "Поиск не находит документы",
		Premise:   "Поиск отвечает ошибкой на часть запросов, свежие документы не появляются в выдаче.",
		RestoreAt: 3,
		FinalOK:   []string{"t3verify"},
		Debrief:   "Причина была в рассинхронизации индекса и документов. Откат вернул совместимую версию, пересборка индекса восстановила выдачу, проверка подтвердила результат на тестовом наборе.",
		Turns: []turn{
			{
				Prompt: "Первые обращения падают с ошибкой 500. Что делаем до разбора логов?",
				Clue:   "В логах видно: документы записаны после последней пересборки индекса. Приложение обновилось, индекс остался старым.",
				Options: []option{
					{
						ID:           "t1rollback",
						Label:        "Откатить выкладку",
						Conservative: true,
						GoodDelta:    1,
						GoodEffect:   "Откат вернул прошлую версию, поиск снова отвечает без ошибок.",
					},
					{
						ID:         "t1cache",
						Label:      "Очистить кэш",
						GoodDelta:  -1,
						GoodEffect: "Кэш очищен, но запросы пошли прямо в несовместимый индекс.",
					},
					{
						ID:         "t1restart",
						Label:      "Перезапустить поиск",
						GoodDelta:  0,
						GoodEffect: "Поиск перезапустился, ошибки 500 остались.",
					},
				},
			},
			{
				Prompt: "Ошибки ушли, но свежие документы всё ещё не находятся. Следующий шаг?",
				Clue:   "Счётчик индекса отстаёт на 12 минут. Ошибок разбора нет, документы на месте.",
				Options: []option{
					{
						ID:         "t2reindex",
						Label:      "Пересобрать индекс",
						Requires:   []string{"t1rollback"},
						GoodDelta:  2,
						GoodEffect: "Индекс пересобран из документов совместимой версии, выдача снова полная.",
						BadDelta:   1,
						BadEffect:  "Индекс пересобран, но часть полей новой версии не совпала: выдача частичная.",
					},
					{
						ID:         "t2timeout",
						Label:      "Увеличить таймаут",
						GoodDelta:  -1,
						GoodEffect: "Таймаут вырос, запросы стали ждать дольше, ошибка осталась.",
					},
					{
						ID:           "t2wait",
						Label:        "Подождать пересборку",
						Conservative: true,
						GoodDelta:    0,
						GoodEffect:   "Индекс сам не пересобрался, документы остались невидимыми.",
					},
				},
			},
			{
				Prompt: "Выдача отвечает, но часть документов пропадает. Чем закроем смену?",
				Clue:   "Тестовый набор из 20 документов показывает 17 результатов. Не хватает трёх свежих записей.",
				Options: []option{
					{
						ID:         "t3verify",
						Label:      "Проверить выдачу на тестах",
						Requires:   []string{"t2reindex"},
						GoodDelta:  2,
						GoodEffect: "Проверка на тестовом наборе прошла: все 20 документов находятся.",
						BadDelta:   0,
						BadEffect:  "Проверка не запускалась: индекс не пересобран, сравнивать нечего.",
					},
					{
						ID:         "t3oldindex",
						Label:      "Вернуть старый индекс",
						GoodDelta:  -1,
						GoodEffect: "Старый индекс вернул прежние пропуски в выдаче.",
					},
					{
						ID:           "t3announce",
						Label:        "Объявить восстановление",
						Conservative: true,
						GoodDelta:    -1,
						GoodEffect:   "Восстановление объявлено до проверки, часть документов всё ещё не находится.",
					},
				},
			},
		},
	}
}

func queueScenario() scenario {
	return scenario{
		ID:        "queue",
		Version:   scenarioVersion,
		Title:     "Очередь задач не разбирается",
		Premise:   "Очередь растёт, время ожидания превышает час, часть задач остаётся без результата.",
		RestoreAt: 3,
		FinalOK:   []string{"t3intake"},
		Debrief:   "Очередь стояла из-за блокировки воркеров, а не из-за нехватки рук. Остановка приёма дала запас, снятие блокировки вернуло обработку, включение приёма закрепило результат.",
		Turns: []turn{
			{
				Prompt: "Очередь растёт третий час. Что делаем первым?",
				Clue:   "Метрики: три воркера простаивают, самая старая задача ждёт 40 минут. Лимит соединений не достигнут.",
				Options: []option{
					{
						ID:           "t1pause",
						Label:        "Остановить приём",
						Conservative: true,
						GoodDelta:    1,
						GoodEffect:   "Приём остановлен, очередь перестала расти.",
					},
					{
						ID:         "t1workers",
						Label:      "Увеличить число воркеров",
						GoodDelta:  -1,
						GoodEffect: "Новые воркеры встали в ту же блокировку, очередь не уменьшилась.",
					},
					{
						ID:         "t1flush",
						Label:      "Перезапустить очередь",
						GoodDelta:  -2,
						GoodEffect: "Перезапуск потерял задачи в работе, очередь стала короче без результата.",
					},
				},
			},
			{
				Prompt: "Приток остановлен, но очередь не движется. Где искать причину?",
				Clue:   "В логах воркеров одна и та же блокировка на общем хранилище. Задачи снимаются по одной, но обработка не начинается.",
				Options: []option{
					{
						ID:         "t2unlock",
						Label:      "Снять блокировку",
						Requires:   []string{"t1pause"},
						GoodDelta:  2,
						GoodEffect: "Блокировка снята, воркеры снова берут задачи, очередь пошла вниз.",
						BadDelta:   1,
						BadEffect:  "Блокировка снята, но новые задачи продолжают поступать и очередь снова растёт.",
					},
					{
						ID:         "t2more",
						Label:      "Добавить ещё воркеров",
						GoodDelta:  -1,
						GoodEffect: "Дополнительные воркеры упёрлись в ту же блокировку.",
					},
					{
						ID:           "t2purge",
						Label:        "Очистить старые задачи",
						Conservative: true,
						GoodDelta:    -2,
						GoodEffect:   "Старые задачи удалены, часть результатов потеряна.",
					},
				},
			},
			{
				Prompt: "Очередь разбирается, время ожидания вернулось в норму. Чем закроем смену?",
				Clue:   "Среднее ожидание 4 минуты. Воркеры свободны, блокировка не возвращается.",
				Options: []option{
					{
						ID:         "t3intake",
						Label:      "Включить приём",
						Requires:   []string{"t2unlock"},
						GoodDelta:  2,
						GoodEffect: "Приём включён, очередь держится в пределах нормы.",
						BadDelta:   0,
						BadEffect:  "Приём включён, но блокировка не снята: очередь снова растёт.",
					},
					{
						ID:           "t3manual",
						Label:        "Разобрать старые задачи вручную",
						Conservative: true,
						GoodDelta:    1,
						GoodEffect:   "Старые задачи разобраны вручную; приём пока закрыт.",
					},
					{
						ID:         "t3closed",
						Label:      "Оставить приём закрытым",
						GoodDelta:  0,
						GoodEffect: "Приём остался закрытым; новые задачи не принимаются.",
					},
				},
			},
		},
	}
}

func certsScenario() scenario {
	return scenario{
		ID:        "certs",
		Version:   scenarioVersion,
		Title:     "Ротация сертификата сломала рукопожатие",
		Premise:   "После ротации часть клиентов не подключается, в логах ошибки проверки цепочки.",
		RestoreAt: 3,
		FinalOK:   []string{"t3handshake"},
		Debrief:   "Ротация ломалась на неполной цепочке. Откат вернул рабочий сертификат, раздача полной цепочки восстановила доверие, проверка рукопожатия подтвердила результат.",
		Turns: []turn{
			{
				Prompt: "После ротации часть клиентов не подключается. Первый шаг?",
				Clue:   "В логах: клиенты не видят промежуточный сертификат. Старый сертификат истекает через час.",
				Options: []option{
					{
						ID:           "t1rollback",
						Label:        "Откатить сертификат",
						Conservative: true,
						GoodDelta:    1,
						GoodEffect:   "Откат вернул старый сертификат, клиенты снова подключаются.",
					},
					{
						ID:         "t1both",
						Label:      "Добавить новый сертификат рядом",
						GoodDelta:  -1,
						GoodEffect: "Два сертификата в доверенном хранилище дали неоднозначную цепочку.",
					},
					{
						ID:         "t1ignore",
						Label:      "Игнорировать предупреждение",
						GoodDelta:  -2,
						GoodEffect: "Ошибки рукопожатия выросли, часть клиентов отключилась.",
					},
				},
			},
			{
				Prompt: "Соединения восстановились, но старый сертификат истекает через час. Что делаем?",
				Clue:   "Новый сертификат выпущен. Часть узлов получила только листовой сертификат без промежуточного.",
				Options: []option{
					{
						ID:         "t2chain",
						Label:      "Раздать полную цепочку",
						Requires:   []string{"t1rollback"},
						GoodDelta:  2,
						GoodEffect: "Полная цепочка роздана узлам, клиенты видят оба сертификата.",
						BadDelta:   1,
						BadEffect:  "Цепочка роздана, но часть узлов не приняла корень.",
					},
					{
						ID:         "t2prolong",
						Label:      "Продлить старый сертификат",
						GoodDelta:  -1,
						GoodEffect: "Старый сертификат продлён, новый так и не роздан.",
					},
					{
						ID:           "t2restart",
						Label:        "Перезапустить узлы",
						Conservative: true,
						GoodDelta:    0,
						GoodEffect:   "Узлы перезапущены, ошибка рукопожатия осталась.",
					},
				},
			},
			{
				Prompt: "Новый сертификат установлен. Чем подтвердим завершение ротации?",
				Clue:   "Тестовый клиент проходит рукопожатие. Осталось проверить все узлы, включая резервный.",
				Options: []option{
					{
						ID:         "t3handshake",
						Label:      "Проверить рукопожатие на всех узлах",
						Requires:   []string{"t2chain"},
						GoodDelta:  2,
						GoodEffect: "Рукопожатие проверено на всех узлах, включая резервный: ошибок нет.",
						BadDelta:   0,
						BadEffect:  "Проверка не запускалась: цепочка не роздана, проверять нечего.",
					},
					{
						ID:           "t3done",
						Label:        "Считать ротацию завершённой",
						Conservative: true,
						GoodDelta:    -1,
						GoodEffect:   "Ротация объявлена завершённой до проверки, часть узлов ещё не обновилась.",
					},
					{
						ID:         "t3old",
						Label:      "Вернуть старый сертификат",
						GoodDelta:  -1,
						GoodEffect: "Старый сертификат возвращён, новый так и не заработал.",
					},
				},
			},
		},
	}
}
