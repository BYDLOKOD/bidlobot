package prod

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/gameengine"
)

const maxParticipantNames = 10

func helpView() gameengine.Result {
	return gameengine.Result{Text: strings.Join([]string{
		"Прод не спит",
		fmt.Sprintf("Резерв на запуск: %d репутаций. На сбор команды есть 90 секунд.", SponsorReservation),
		"Нужны минимум три участника. Дальше три хода по 45 секунд. Участие и голоса бесплатны.",
	}, "\n"), Buttons: []gameengine.Button{openButton("Открыть инцидент")}}
}

func openButton(label string) gameengine.Button {
	return gameengine.Button{Label: fmt.Sprintf("%s за %d репутаций", label, SponsorReservation), Verb: VerbOpen, Arg: strconv.Itoa(SponsorReservation)}
}

func incidentView(inc *incident, now time.Time) gameengine.Result {
	switch inc.Phase {
	case phaseLobby:
		return lobbyView(inc, now)
	case phaseRunning:
		return runningView(inc, now)
	default:
		return finalView(inc, now)
	}
}

func lobbyView(inc *incident, now time.Time) gameengine.Result {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n%s\n\nСбор команды\n", inc.Scenario.Title, inc.Scenario.Premise)
	fmt.Fprintf(&b, "Дежурный: Участник %d\n", inc.Sponsor)
	if len(inc.Participants) == 0 {
		b.WriteString("Участники: пока никого\n")
	} else {
		fmt.Fprintf(&b, "Участники (%d): %s\n", len(inc.Participants), participantNames(inc.Participants))
	}
	if len(uniqueParticipants(inc.Participants)) >= 3 {
		b.WriteString("Участников достаточно. Дежурный может начать.\n")
	} else {
		b.WriteString("Для старта нужно три участника.\n")
	}
	fmt.Fprintf(&b, "До закрытия: %s\n", remaining(now, inc.LobbyDeadline))
	fmt.Fprintf(&b, "Резерв репутации: %d. Сумма спишется при старте. Если инцидент не начнётся, резерв вернётся.\n", inc.sponsorCost())
	b.WriteString("Участие бесплатное. Решения и голоса попадут в разбор чата на 30 дней.")

	return gameengine.Result{
		Text: b.String(),
		Buttons: []gameengine.Button{
			{Label: "Присоединиться", Verb: VerbJoin, Arg: fmt.Sprintf("%s:0", inc.ID)},
			{Label: "Начать", Verb: VerbStart, Arg: fmt.Sprintf("%s:0", inc.ID)},
			{Label: "Отменить", Verb: VerbCancel, Arg: fmt.Sprintf("%s:0", inc.ID)},
		},
	}
}

func runningView(inc *incident, now time.Time) gameengine.Result {
	turn := inc.Scenario.Turns[inc.Turn-1]
	signal, evidence, clue := turn.reading(inc.Applied)
	var b strings.Builder
	fmt.Fprintf(&b, "%s\nХод %d из %d. До конца: %s\n\n", inc.Scenario.Title, inc.Turn, len(inc.Scenario.Turns), remaining(now, inc.TurnDeadline))
	fmt.Fprintf(&b, "Сигнал: %s\n", signal)
	if evidence != "" {
		fmt.Fprintf(&b, "Что видно: %s\n", evidence)
	}
	if inc.ClueTurn == inc.Turn {
		fmt.Fprintf(&b, "Подсказка: %s\n", clue)
	}
	if len(inc.TurnResults) != 0 {
		last := inc.TurnResults[len(inc.TurnResults)-1]
		if last.NoVotes {
			b.WriteString("На прошлом ходу никто не проголосовал.\n")
		} else {
			fmt.Fprintf(&b, "После прошлого хода: %s\n", last.Effect)
		}
	}
	fmt.Fprintf(&b, "\nОчки восстановления: %d. Нужно не меньше %d и подтверждение на последнем ходу.\n",
		inc.Score, inc.Scenario.RestoreAt)
	b.WriteByte('\n')
	counts := make(map[string]int, len(turn.Options))
	for _, optID := range inc.Votes {
		counts[optID]++
	}
	buttons := make([]gameengine.Button, 0, len(turn.Options)+1)
	for i, opt := range turn.Options {
		delta, _ := opt.effect(inc.Applied)
		label := fmt.Sprintf("%s (%s)", opt.Label, scoreDelta(delta))
		fmt.Fprintf(&b, "%d. %s. Голоса: %d", i+1, label, counts[opt.ID])
		if opt.Conservative {
			b.WriteString(" (приоритет при ничьей)")
		}
		b.WriteByte('\n')
		buttons = append(buttons, gameengine.Button{
			Label: label,
			Verb:  VerbVote,
			Arg:   fmt.Sprintf("%s:%d:%s", inc.ID, inc.Turn, opt.ID),
		})
	}
	b.WriteString("\nПобеждает вариант с наибольшим числом голосов. При ничьей побеждает вариант с приоритетом, если он среди лидеров, или первый из лидеров по списку.\nГолос можно изменить до конца хода. Без голосов состояние не меняется.")
	if inc.ClueTurn == 0 && clue != "" {
		buttons = append(buttons, gameengine.Button{
			Label: "Подсказка Хранителя прода",
			Verb:  VerbClue,
			Arg:   fmt.Sprintf("%s:%d", inc.ID, inc.Turn),
		})
	}
	return gameengine.Result{Text: b.String(), Buttons: buttons}
}

func finalView(inc *incident, now time.Time) gameengine.Result {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\nИтог: %s.\n", inc.Scenario.Title, outcomeLabel(inc.Outcome))
	if inc.Outcome == outcomeCancelled {
		if inc.CancelReason != "" {
			fmt.Fprintf(&b, "\n%s\n", inc.CancelReason)
		}
	} else {
		fmt.Fprintf(&b, "\nОчки восстановления: %d. Порог: %d.\n", inc.Score, inc.Scenario.RestoreAt)
		if inc.Scenario.Version >= 2 {
			confirmed := "нет"
			if recoveryConfirmed(inc) {
				confirmed = "есть"
			}
			fmt.Fprintf(&b, "Подтверждение: %s.\n", confirmed)
		}
		if inc.Scenario.Cause != "" {
			fmt.Fprintf(&b, "Причина сбоя: %s\n", inc.Scenario.Cause)
		}
		for _, result := range inc.TurnResults {
			if result.NoVotes {
				fmt.Fprintf(&b, "\nХод %d. Голосов не было. Состояние не изменилось.\n", result.Turn)
				continue
			}
			fmt.Fprintf(&b, "\nХод %d. %s (%s). %s\n", result.Turn, result.OptionLabel, scoreDelta(result.Delta), result.Effect)
			if result.Tie {
				fmt.Fprintf(&b, "%s\n", tieReason(inc, result.Turn, result.OptionID))
			}
		}
	}
	if !now.Before(inc.HistoryUntil) {
		b.WriteString("\nРазбор больше не хранится.")
		return gameengine.Result{Text: b.String()}
	}
	b.WriteString("\nРазбор хранится 30 дней.")

	return gameengine.Result{
		Text: b.String(),
		Buttons: []gameengine.Button{{
			Label: "Ретроспектива",
			Verb:  VerbRetro,
			Arg:   fmt.Sprintf("%s:%d:1", inc.ID, finalVersion(inc)),
		}},
	}
}

func retrospective(st *bolt.Bucket, a gameengine.Action) (gameengine.Result, error) {
	parts := strings.Split(a.Arg, ":")
	if len(parts) != 3 || parts[0] == "" {
		return gameengine.Result{}, gameengine.Reject("Страница разбора недоступна. Откройте карточку заново.")
	}
	version, err := strconv.Atoi(parts[1])
	if err != nil {
		return gameengine.Result{}, gameengine.Reject("Страница разбора недоступна. Откройте карточку заново.")
	}
	page, err := strconv.Atoi(parts[2])
	if err != nil {
		return gameengine.Result{}, gameengine.Reject("Страница разбора недоступна. Откройте карточку заново.")
	}
	inc, err := loadIncident(st, parts[0])
	if err != nil {
		return gameengine.Result{}, err
	}
	if inc == nil {
		return gameengine.Result{}, gameengine.Reject("Разбор не найден. Откройте карточку инцидента.")
	}
	if inc.Phase != phaseFinal {
		return gameengine.Result{}, gameengine.Reject("Разбор появится после итога инцидента.")
	}
	if version != finalVersion(inc) {
		return gameengine.Result{}, gameengine.Reject("Карточка устарела. Откройте итог инцидента заново.")
	}
	if inc.HistoryUntil.IsZero() || !a.Now.Before(inc.HistoryUntil) {
		return gameengine.Result{}, gameengine.Reject("Разбор больше не хранится.")
	}
	entries, err := loadHistory(st, inc.ID)
	if err != nil {
		return gameengine.Result{}, err
	}
	pages := (len(entries) + RetroPageSize - 1) / RetroPageSize
	if pages == 0 {
		pages = 1
	}
	if page < 1 || page > pages {
		return gameengine.Result{}, gameengine.Reject("Страница разбора недоступна. Откройте карточку заново.")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Разбор инцидента \"%s\"\n", inc.Scenario.Title)
	fmt.Fprintf(&b, "Итог: %s.\n\n", outcomeLabel(inc.Outcome))
	start := (page - 1) * RetroPageSize
	end := start + RetroPageSize
	if end > len(entries) {
		end = len(entries)
	}
	if len(entries) == 0 {
		b.WriteString("Принятых действий нет.\n")
	}
	for _, entry := range entries[start:end] {
		b.WriteString(renderHistoryEntry(inc, entry))
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Страница %d из %d.", page, pages)

	buttons := make([]gameengine.Button, 0, 2)
	versionPart := strconv.Itoa(finalVersion(inc))
	if page > 1 {
		buttons = append(buttons, gameengine.Button{
			Label: "Назад",
			Verb:  VerbRetro,
			Arg:   fmt.Sprintf("%s:%s:%d", inc.ID, versionPart, page-1),
		})
	}
	if page < pages {
		buttons = append(buttons, gameengine.Button{
			Label: "Вперёд",
			Verb:  VerbRetro,
			Arg:   fmt.Sprintf("%s:%s:%d", inc.ID, versionPart, page+1),
		})
	}
	return gameengine.Result{Text: b.String(), Buttons: buttons}, nil
}

func renderHistoryEntry(inc *incident, e historyEntry) string {
	switch e.Kind {
	case kindOpen:
		return fmt.Sprintf("Участник %d: открыт сбор команды.", e.Actor)
	case kindJoin:
		return fmt.Sprintf("Участник %d в команде.", e.Actor)
	case kindStart:
		return fmt.Sprintf("Участник %d: инцидент запущен.", e.Actor)
	case kindVote:
		if e.PreviousLabel != "" {
			return fmt.Sprintf("Ход %d. Участник %d: голос изменён с \"%s\" на \"%s\".",
				e.Turn, e.Actor, e.PreviousLabel, e.OptionLabel)
		}
		return fmt.Sprintf("Ход %d. Участник %d: голос за \"%s\".", e.Turn, e.Actor, e.OptionLabel)
	case kindClue:
		return fmt.Sprintf("Ход %d. Участник %d: открыта подсказка. %s", e.Turn, e.Actor, e.Clue)
	case kindResult:
		if e.OptionLabel == "" {
			return fmt.Sprintf("Ход %d. Голосов не было. Состояние не изменилось.", e.Turn)
		}
		line := fmt.Sprintf("Ход %d. Выполнили: %s. Голоса по порядку вариантов: %s. %s", e.Turn, e.OptionLabel, e.Counts, e.Effect)
		if e.Tie {
			line += " " + tieReason(inc, e.Turn, e.OptionID)
		}
		return line
	default:
		return e.Effect
	}
}

func outcomeLabel(outcome string) string {
	switch outcome {
	case outcomeRestored:
		return "сервис восстановлен"
	case outcomeUnresolved:
		return "сервис не восстановлен"
	case outcomeCancelled:
		return "отменён"
	default:
		return "итог не определён"
	}
}

func scoreDelta(delta int) string {
	if delta > 0 {
		return fmt.Sprintf("+%d", delta)
	}
	return strconv.Itoa(delta)
}

func tieReason(inc *incident, turnNumber int, optionID string) string {
	opt, ok := optionByID(inc.Scenario.Turns[turnNumber-1], optionID)
	if ok && opt.Conservative {
		return "При равенстве голосов победил вариант с приоритетом."
	}
	return "При равенстве голосов победил первый из лидеров по порядку в карточке."
}

func participantNames(ids []int64) string {
	unique := uniqueParticipants(ids)
	if len(unique) == 0 {
		return "пока никого"
	}
	shown := unique
	if len(shown) > maxParticipantNames {
		shown = shown[:maxParticipantNames]
	}
	parts := make([]string, 0, len(shown)+1)
	for _, id := range shown {
		parts = append(parts, fmt.Sprintf("Участник %d", id))
	}
	if len(unique) > len(shown) {
		parts = append(parts, fmt.Sprintf("и ещё %d", len(unique)-len(shown)))
	}
	return strings.Join(parts, ", ")
}

func remaining(now, deadline time.Time) string {
	d := deadline.Sub(now)
	if d <= 0 {
		return "время вышло"
	}
	seconds := (d + time.Second - 1) / time.Second
	return fmt.Sprintf("%d с", seconds)
}

func finalVersion(inc *incident) int {
	return inc.Turn
}
