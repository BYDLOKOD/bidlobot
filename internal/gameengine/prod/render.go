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
		"«Прод не спит» — инцидент на три хода по 45 секунд.",
		fmt.Sprintf("Дежурный может открыть инцидент за %d репы. На сбор команды есть 90 секунд.", SponsorReservation),
		"Нужны три участника. Присоединение и голоса бесплатны.",
	}, "\n"), Buttons: []gameengine.Button{openButton("Открыть инцидент")}}
}

func openButton(label string) gameengine.Button {
	return gameengine.Button{Label: fmt.Sprintf("%s за %d репы", label, SponsorReservation), Verb: VerbOpen, Arg: strconv.Itoa(SponsorReservation)}
}

func incidentView(inc *incident, now time.Time) gameengine.Result {
	switch inc.Phase {
	case phaseLobby:
		return lobbyView(inc, now)
	case phaseRunning:
		return runningView(inc, now)
	default:
		return finalView(inc)
	}
}

func lobbyView(inc *incident, now time.Time) gameengine.Result {
	var b strings.Builder
	fmt.Fprintf(&b, "«%s»\n", inc.Scenario.Title)
	fmt.Fprintf(&b, "%s\n", inc.Scenario.Premise)
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
	fmt.Fprintf(&b, "До закрытия лобби: %s.\n", remaining(now, inc.LobbyDeadline))
	b.WriteString("Ходы и голоса попадут в разбор этого чата на 30 дней.\n")
	fmt.Fprintf(&b, "Резерв дежурного: %d репы.\n", inc.sponsorCost())
	b.WriteString("Присоединение и голоса бесплатны.")

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
	var b strings.Builder
	fmt.Fprintf(&b, "Смена %d из %d · «%s»\n", inc.Turn, len(inc.Scenario.Turns), inc.Scenario.Title)
	b.WriteString(turn.Prompt)
	b.WriteString("\n")
	if inc.ClueTurn == inc.Turn {
		fmt.Fprintf(&b, "Известно: %s\n", turn.Clue)
	}
	fmt.Fprintf(&b, "До конца хода: %s.\n", remaining(now, inc.TurnDeadline))
	counts := make(map[string]int, len(turn.Options))
	for _, opt := range turn.Options {
		counts[opt.ID] = 0
	}
	for _, optID := range inc.Votes {
		counts[optID]++
	}
	fmt.Fprintf(&b, "Голосов: %d.\n", len(inc.Votes))
	for i, opt := range turn.Options {
		label := opt.Label
		if opt.Conservative {
			label += " (осторожный вариант)"
		}
		fmt.Fprintf(&b, "%d. %s — %d\n", i+1, label, counts[opt.ID])
	}
	b.WriteString("Голос можно менять до конца хода. При равенстве сработает осторожный вариант, иначе порядок в карточке.")

	buttons := make([]gameengine.Button, 0, len(turn.Options)+1)
	for _, opt := range turn.Options {
		label := opt.Label
		if opt.Conservative {
			label += " · осторожно"
		}
		buttons = append(buttons, gameengine.Button{
			Label: label,
			Verb:  VerbVote,
			Arg:   fmt.Sprintf("%s:%d:%s", inc.ID, inc.Turn, opt.ID),
		})
	}
	if inc.ClueTurn == 0 {
		buttons = append(buttons, gameengine.Button{
			Label: "Подсказка",
			Verb:  VerbClue,
			Arg:   fmt.Sprintf("%s:%d", inc.ID, inc.Turn),
		})
	}
	return gameengine.Result{Text: b.String(), Buttons: buttons}
}

func finalView(inc *incident) gameengine.Result {
	var b strings.Builder
	fmt.Fprintf(&b, "Инцидент «%s»: %s.\n", inc.Scenario.Title, outcomeLabel(inc.Outcome))
	if inc.Outcome == outcomeCancelled {
		if inc.CancelReason != "" {
			b.WriteString(inc.CancelReason)
			b.WriteString("\n")
		}
	} else {
		fmt.Fprintf(&b, "Причина: %s\n", roundCause(inc))
		b.WriteString("Ходы:\n")
		for _, result := range inc.TurnResults {
			if result.NoVotes {
				fmt.Fprintf(&b, "%d. Голосов не было; ход прошёл без изменений.\n", result.Turn)
				continue
			}
			fmt.Fprintf(&b, "%d. «%s» (%s). %s", result.Turn, result.OptionLabel, result.Counts, result.Effect)
			if result.Tie {
				b.WriteString(" Голоса разделились; сработал порядок в карточке.")
			}
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "Что повлияло: %s\n", inc.Scenario.Debrief)
	}
	b.WriteString("Разбор хранится 30 дней.")

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
	fmt.Fprintf(&b, "Разбор инцидента «%s»\n", inc.Scenario.Title)
	fmt.Fprintf(&b, "Итог: %s.\n", outcomeLabel(inc.Outcome))
	if page == 1 && inc.Scenario.Debrief != "" {
		b.WriteString(inc.Scenario.Debrief)
		b.WriteString("\n")
	}
	start := (page - 1) * RetroPageSize
	end := start + RetroPageSize
	if end > len(entries) {
		end = len(entries)
	}
	if len(entries) == 0 {
		b.WriteString("Принятых действий нет.\n")
	}
	for _, entry := range entries[start:end] {
		b.WriteString(renderHistoryEntry(entry))
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

func renderHistoryEntry(e historyEntry) string {
	switch e.Kind {
	case kindOpen:
		return fmt.Sprintf("Лобби · Участник %d открыл инцидент.", e.Actor)
	case kindJoin:
		return fmt.Sprintf("Лобби · Участник %d присоединился.", e.Actor)
	case kindStart:
		return fmt.Sprintf("Лобби · Участник %d начал инцидент.", e.Actor)
	case kindVote:
		if e.PreviousLabel != "" {
			return fmt.Sprintf("Ход %d · Участник %d: «%s» (изменён с «%s»).",
				e.Turn, e.Actor, e.OptionLabel, e.PreviousLabel)
		}
		return fmt.Sprintf("Ход %d · Участник %d: «%s».", e.Turn, e.Actor, e.OptionLabel)
	case kindClue:
		return fmt.Sprintf("Ход %d · Участник %d открыл подсказку: %s", e.Turn, e.Actor, e.Clue)
	case kindResult:
		if e.OptionLabel == "" {
			return fmt.Sprintf("Ход %d · Голосов не было; ход прошёл без изменений.", e.Turn)
		}
		line := fmt.Sprintf("Ход %d · Выполнили: «%s» (%s). %s", e.Turn, e.OptionLabel, e.Counts, e.Effect)
		if e.Tie {
			line += " Голоса разделились; сработал порядок в карточке."
		}
		return line
	case kindCancel, kindExpire:
		return "Лобби · " + e.Effect
	default:
		return "Лобби · действие учтено."
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

func roundCause(inc *incident) string {
	if len(inc.TurnResults) == 0 {
		return "Ни одно решение не было принято."
	}
	last := inc.TurnResults[len(inc.TurnResults)-1]
	if !last.NoVotes {
		return last.Effect
	}
	for i := len(inc.TurnResults) - 2; i >= 0; i-- {
		if !inc.TurnResults[i].NoVotes {
			return "Последний ход прошёл без голосов. " + inc.TurnResults[i].Effect
		}
	}
	return "Ни одно решение не было принято."
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
		return "меньше секунды"
	}
	seconds := int(d / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return fmt.Sprintf("%d с", seconds)
}

func finalVersion(inc *incident) int {
	return inc.Turn
}
