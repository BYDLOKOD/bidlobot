package prod

// scenario is an authored incident. A deep snapshot of the selected scenario
// is stored inside each incident, so deploying a new build cannot change the
// rules of a lobby or round that is already live.
type scenario struct {
	ID        string   `json:"id"`
	Version   int      `json:"version"`
	Title     string   `json:"title"`
	Premise   string   `json:"premise"`
	Cause     string   `json:"cause,omitempty"`
	RestoreAt int      `json:"restore_at"`
	FinalOK   []string `json:"final_ok"`
	Turns     []turn   `json:"turns"`
}

type turn struct {
	Prompt       string        `json:"prompt"`
	Evidence     string        `json:"evidence,omitempty"`
	Clue         string        `json:"clue"`
	Observations []observation `json:"observations,omitempty"`
	Options      []option      `json:"options"`
}

type observation struct {
	Requires []string `json:"requires"`
	Signal   string   `json:"signal"`
	Evidence string   `json:"evidence"`
	Clue     string   `json:"clue"`
}

func (t turn) reading(applied []string) (signal, evidence, clue string) {
	for _, obs := range t.Observations {
		if hasAll(applied, obs.Requires) {
			return obs.Signal, obs.Evidence, obs.Clue
		}
	}
	return t.Prompt, t.Evidence, t.Clue
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
	if hasAll(applied, o.Requires) {
		return o.GoodDelta, o.GoodEffect
	}
	return o.BadDelta, o.BadEffect
}

func hasAll(applied, requires []string) bool {
	for _, need := range requires {
		if !contains(applied, need) {
			return false
		}
	}
	return true
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
const scenarioVersion = 2

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
		Cause:     "После выкладки приложение перешло на новую схему. Поиск продолжил читать индекс старого формата.",
		Turns: []turn{
			{
				Prompt:   "После выкладки поиск отвечает ошибкой 500 на часть запросов.",
				Evidence: "В журнале поиска ошибки чтения полей. В базе свежие документы есть.",
				Clue:     "Предыдущая версия приложения читает поля в формате, который хранится в индексе.",
				Options: []option{
					{
						ID:           "t1rollback",
						Label:        "Откатить выкладку",
						Conservative: true,
						GoodDelta:    1,
						GoodEffect:   "Откат вернул старую схему. Поиск отвечает без ошибок 500.",
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
				Prompt:   "На сервере осталась новая схема. Индекс хранит поля в старом формате.",
				Evidence: "Проверка совместимости находит три поля новой схемы, которых нет в индексе.",
				Clue:     "Пересборка на новой схеме обновит документы со знакомыми полями. Остальные пропустит.",
				Observations: []observation{
					{
						Requires: []string{"t1rollback"},
						Signal:   "Работает старая схема. Ошибок 500 нет, но индекс отстаёт на 12 минут.",
						Evidence: "Поиск снова читает поля индекса. Свежие документы в базе есть.",
						Clue:     "Проверка на копии базы подтвердила: старой схеме соответствуют все 20 документов.",
					},
				},
				Options: []option{
					{
						ID:         "t2reindex",
						Label:      "Пересобрать индекс",
						Requires:   []string{"t1rollback"},
						GoodDelta:  2,
						GoodEffect: "Индекс пересобран из документов совместимой версии, выдача снова полная.",
						BadDelta:   1,
						BadEffect:  "При пересборке индекс пропустил три документа с несовместимыми полями.",
					},
					{
						ID:         "t2timeout",
						Label:      "Увеличить таймаут",
						GoodDelta:  -1,
						GoodEffect: "Поиск дольше ждёт ответа. Таймаут не меняет схему и не обновляет индекс.",
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
				Prompt:   "Индекс не пересобирали. В выдаче всё ещё нет свежих документов.",
				Evidence: "Пробный запрос находит 17 из 20 документов.",
				Clue:     "В индексе нет трёх свежих документов. Повторный запрос не добавит их.",
				Observations: []observation{
					{
						Requires: []string{"t1rollback", "t2reindex"},
						Signal:   "Старая схема и новый индекс совместимы. Пробный запрос находит 20 из 20 документов.",
						Evidence: "Осталось проверить весь набор запросов.",
						Clue:     "В тестовый набор входят запросы с полями, на которых начался сбой.",
					},
					{
						Requires: []string{"t2reindex"},
						Signal:   "Индекс пересобрали на новой схеме. Пробный запрос находит 17 из 20 документов.",
						Evidence: "В журнале пересборки осталось три пропуска.",
						Clue:     "Во всех трёх документах встречаются поля, которых нет в индексе.",
					},
					{
						Requires: []string{"t1rollback"},
						Signal:   "Работает старая схема, но индекс не пересобирали.",
						Evidence: "Пробный запрос находит 17 из 20 документов.",
						Clue:     "Три свежих документа есть в базе. В индекс они ещё не попали.",
					},
				},
				Options: []option{
					{
						ID:         "t3verify",
						Label:      "Проверить выдачу",
						Requires:   []string{"t1rollback", "t2reindex"},
						GoodDelta:  2,
						GoodEffect: "Проверка прошла: все 20 документов нашлись, ошибок 500 нет.",
						BadDelta:   0,
						BadEffect:  "Проверка нашла 17 из 20 документов. Полную выдачу не подтвердили.",
					},
					{
						ID:         "t3oldindex",
						Label:      "Вернуть старый индекс",
						GoodDelta:  -1,
						GoodEffect: "Выбрана старая версия индекса, в которой нет свежих документов.",
					},
					{
						ID:           "t3announce",
						Label:        "Объявить восстановление",
						Conservative: true,
						GoodDelta:    -1,
						GoodEffect:   "Команда объявила восстановление без проверки выдачи.",
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
		Cause:     "Воркеры ждут одну и ту же блокировку в хранилище. Новые воркеры тоже остановятся на ней.",
		Turns: []turn{
			{
				Prompt:   "Задачи поступают, а воркеры не заканчивают обработку.",
				Evidence: "Три воркера ждут ответа от хранилища. Лимит соединений не достигнут.",
				Clue:     "После остановки приёма задачи в очереди останутся на месте.",
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
				Prompt:   "Новые задачи продолжают поступать, воркеры ждут.",
				Evidence: "В журнале у всех воркеров одна и та же блокировка.",
				Clue:     "Если снять блокировку при открытом приёме, воркеры возобновят работу, но очередь продолжит расти.",
				Observations: []observation{
					{
						Requires: []string{"t1pause"},
						Signal:   "Приём остановлен. Размер очереди не меняется.",
						Evidence: "В журнале у всех воркеров одна и та же блокировка.",
						Clue:     "Хранилище показывает владельца блокировки: процесс уже завершился.",
					},
				},
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
				Prompt:   "Воркеры до сих пор ждут блокировку. Нормальная обработка не возобновилась.",
				Evidence: "В журнале нет новых успешных завершений задач.",
				Clue:     "При открытом приёме новые задачи будут скапливаться на той же блокировке.",
				Observations: []observation{
					{
						Requires: []string{"t1pause", "t2unlock"},
						Signal:   "Приём закрыт, воркеры снова разбирают очередь.",
						Evidence: "Среднее ожидание упало до 4 минут.",
						Clue:     "Воркеры обрабатывают больше задач в минуту, чем поступало до сбоя.",
					},
					{
						Requires: []string{"t2unlock"},
						Signal:   "Воркеры снова работают, но открытый приём мешает сократить очередь.",
						Evidence: "В очереди ещё слишком много задач.",
						Clue:     "Очередь уменьшится, если на время остановить поступление задач.",
					},
					{
						Requires: []string{"t1pause"},
						Signal:   "Приём закрыт, но воркеры по-прежнему заблокированы.",
						Evidence: "В журнале нет новых успешных завершений задач.",
						Clue:     "Закрытый приём сдерживает рост очереди. Блокировку нужно снять отдельно.",
					},
				},
				Options: []option{
					{
						ID:         "t3intake",
						Label:      "Открыть приём",
						Requires:   []string{"t1pause", "t2unlock"},
						GoodDelta:  2,
						GoodEffect: "Приём включён, очередь держится в пределах нормы.",
						BadDelta:   0,
						BadEffect:  "Приём открыт, но время ожидания не вернулось в норму.",
					},
					{
						ID:           "t3manual",
						Label:        "Разобрать старые задачи вручную",
						Conservative: true,
						GoodDelta:    1,
						GoodEffect:   "Часть старых задач разобрали вручную. Режим приёма не меняли.",
					},
					{
						ID:         "t3closed",
						Label:      "Закрыть приём",
						GoodDelta:  0,
						GoodEffect: "Приём закрыт. Новые задачи не принимаются.",
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
		Cause:     "Часть узлов получила новый сертификат без промежуточного. Клиенты не смогли проверить цепочку.",
		Turns: []turn{
			{
				Prompt:   "После ротации часть клиентов перестала подключаться.",
				Evidence: "Клиентам не хватает промежуточного сертификата. Старый истекает через час.",
				Clue:     "Старый сертификат ещё действителен, и узлы сохранили его полную цепочку.",
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
				Prompt:   "Ошибки проверки цепочки продолжаются. Рабочую конфигурацию не вернули.",
				Evidence: "На части узлов установлен новый сертификат без промежуточного.",
				Clue:     "Без единой рабочей конфигурации не все узлы применят обновление цепочки.",
				Observations: []observation{
					{
						Requires: []string{"t1rollback"},
						Signal:   "Соединения восстановились после отката. Старый сертификат истекает через час.",
						Evidence: "Хранилище содержит новый сертификат и промежуточный к нему.",
						Clue:     "Резервный узел проверяет цепочку так же, как основной, но обновляется отдельно.",
					},
				},
				Options: []option{
					{
						ID:         "t2chain",
						Label:      "Раздать полную цепочку",
						Requires:   []string{"t1rollback"},
						GoodDelta:  2,
						GoodEffect: "Узлы получили новый сертификат вместе с промежуточным.",
						BadDelta:   1,
						BadEffect:  "Часть узлов не применила новую цепочку. Ошибки подключения остались.",
					},
					{
						ID:         "t2prolong",
						Label:      "Продлить старый сертификат",
						GoodDelta:  -1,
						GoodEffect: "Продлили старый сертификат. Конфигурацию узлов не меняли.",
					},
					{
						ID:           "t2restart",
						Label:        "Перезапустить узлы",
						Conservative: true,
						GoodDelta:    0,
						GoodEffect:   "Узлы перезапустились с прежними настройками.",
					},
				},
			},
			{
				Prompt:   "Полную цепочку нового сертификата не раздавали.",
				Evidence: "В настройках части узлов по-прежнему нет полной цепочки.",
				Clue:     "Ответ одного узла ничего не говорит о сертификате на остальных.",
				Observations: []observation{
					{
						Requires: []string{"t1rollback", "t2chain"},
						Signal:   "На узлах установили новый сертификат с промежуточным.",
						Evidence: "Пробное подключение к основному узлу прошло.",
						Clue:     "Проверка должна отдельно подключиться к резервному узлу.",
					},
					{
						Requires: []string{"t2chain"},
						Signal:   "Новую цепочку применили не все узлы.",
						Evidence: "Пробное подключение к одному из узлов всё ещё не проходит.",
						Clue:     "На этом узле всё ещё нет промежуточного сертификата.",
					},
					{
						Requires: []string{"t1rollback"},
						Signal:   "Узлы всё ещё работают на старом сертификате.",
						Evidence: "Новый сертификат с полной цепочкой не установлен.",
						Clue:     "Проверка текущих подключений покажет работу старого сертификата.",
					},
				},
				Options: []option{
					{
						ID:         "t3handshake",
						Label:      "Проверить рукопожатие на всех узлах",
						Requires:   []string{"t1rollback", "t2chain"},
						GoodDelta:  2,
						GoodEffect: "Рукопожатие проверено на всех узлах, включая резервный: ошибок нет.",
						BadDelta:   0,
						BadEffect:  "Проверка не подтвердила работу нового сертификата на всех узлах.",
					},
					{
						ID:           "t3done",
						Label:        "Считать ротацию завершённой",
						Conservative: true,
						GoodDelta:    -1,
						GoodEffect:   "Команда объявила ротацию завершённой без проверки всех узлов.",
					},
					{
						ID:         "t3old",
						Label:      "Вернуть старый сертификат",
						GoodDelta:  -1,
						GoodEffect: "Узлы вернулись на старый сертификат. Ротация не завершена.",
					},
				},
			},
		},
	}
}
