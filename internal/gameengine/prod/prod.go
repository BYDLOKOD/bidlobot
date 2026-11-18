// Package prod implements the "Прод не спит" incident game: a sponsor opens a
// ninety-second lobby, three participants vote through three forty-five-second
// turns, and every accepted action is kept in a chat-scoped retrospective.
package prod

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/domain/reputation"
	"github.com/veschin/bidlobot/internal/gameengine"
)

// ModuleName is the engine registration key and the state bucket name.
const ModuleName = "prod"

const (
	// LobbyLifetime is how long a sponsor waits for three participants.
	LobbyLifetime = 90 * time.Second
	// TurnLifetime is the voting window of one turn.
	TurnLifetime = 45 * time.Second
	// TurnCount is the number of turns in an incident.
	TurnCount = 3
	// SponsorReservation is the reputation held when a lobby opens.
	SponsorReservation = 18
	// SponsorScore is the title score required to sponsor an incident.
	SponsorScore = reputation.DutyThreshold
	// HistoryRetention is how long the retrospective stays readable.
	HistoryRetention = 30 * 24 * time.Hour
	// RetroPageSize bounds one retrospective page.
	RetroPageSize = 8
)

// Verbs are callback and command actions. Button Arg always carries the
// incident ID and the expected turn/version, so an old button is refused.
const (
	VerbShow   = "show"
	VerbOpen   = "open"
	VerbJoin   = "join"
	VerbStart  = "start"
	VerbCancel = "cancel"
	VerbVote   = "vote"
	VerbClue   = "clue"
	VerbRetro  = "retro"
	VerbTick   = "tick"
)

const (
	phaseLobby   = "lobby"
	phaseRunning = "running"
	phaseFinal   = "final"

	outcomeRestored   = "restored"
	outcomeUnresolved = "unresolved"
	outcomeCancelled  = "cancelled"
)

const (
	kindOpen   = "open"
	kindJoin   = "join"
	kindStart  = "start"
	kindVote   = "vote"
	kindClue   = "clue"
	kindResult = "result"
	kindCancel = "cancel"
	kindExpire = "expire"
)

var (
	keyActive   = []byte("active")
	keyLast     = []byte("last")
	keySeq      = []byte("seq")
	keyRotation = []byte("rotation")
)

// sponsorMinScore is a var so tests can exercise the round without seeding a
// full title history. Production keeps the published threshold.
var sponsorMinScore = SponsorScore

type incident struct {
	ID            string    `json:"id"`
	Scenario      scenario  `json:"scenario"`
	Phase         string    `json:"phase"`
	Sponsor       int64     `json:"sponsor"`
	SponsorCost   int       `json:"sponsor_cost,omitempty"`
	Participants  []int64   `json:"participants"`
	CreatedAt     time.Time `json:"created_at"`
	LobbyDeadline time.Time `json:"lobby_deadline"`

	StartedAt    time.Time        `json:"started_at"`
	Turn         int              `json:"turn"`
	TurnDeadline time.Time        `json:"turn_deadline"`
	Votes        map[int64]string `json:"votes"`
	ClueTurn     int              `json:"clue_turn"`
	Applied      []string         `json:"applied"`
	TurnResults  []turnResult     `json:"turn_results"`
	Score        int              `json:"score"`

	Outcome      string    `json:"outcome"`
	CancelReason string    `json:"cancel_reason,omitempty"`
	FinishedAt   time.Time `json:"finished_at"`
	HistoryUntil time.Time `json:"history_until"`
	HistorySeq   int64     `json:"history_seq"`
}

func (inc *incident) sponsorCost() int {
	if inc.SponsorCost == 0 {
		return 3 // Lobbies already opened under the previous price.
	}
	return inc.SponsorCost
}

type turnResult struct {
	Turn        int    `json:"turn"`
	OptionID    string `json:"option_id,omitempty"`
	OptionLabel string `json:"option_label,omitempty"`
	Counts      string `json:"counts,omitempty"`
	Tie         bool   `json:"tie,omitempty"`
	NoVotes     bool   `json:"no_votes,omitempty"`
	Effect      string `json:"effect"`
	Delta       int    `json:"delta"`
}

type historyEntry struct {
	Seq           int64     `json:"seq"`
	Turn          int       `json:"turn"`
	At            time.Time `json:"at"`
	Actor         int64     `json:"actor"`
	Kind          string    `json:"kind"`
	OptionID      string    `json:"option_id,omitempty"`
	OptionLabel   string    `json:"option_label,omitempty"`
	PreviousLabel string    `json:"previous_label,omitempty"`
	Clue          string    `json:"clue,omitempty"`
	Effect        string    `json:"effect,omitempty"`
	Counts        string    `json:"counts,omitempty"`
	Tie           bool      `json:"tie,omitempty"`
}

// Apply is the engine module entry point.
func Apply(tx *bolt.Tx, a gameengine.Action) (gameengine.Result, error) {
	if a.Now.IsZero() {
		a.Now = time.Now()
	}
	a.Now = a.Now.UTC()
	if a.Verb == VerbTick {
		if a.UserID != 0 {
			return gameengine.Result{}, gameengine.Reject("Это действие выполняет бот.")
		}
		return tick(tx, a)
	}
	if a.ChatID <= 0 || a.UserID <= 0 {
		return gameengine.Result{}, gameengine.Reject("Некорректный чат или участник.")
	}
	st, err := gameengine.State(tx, a.ChatID, ModuleName)
	if err != nil {
		return gameengine.Result{}, err
	}
	if st == nil {
		if a.Verb == VerbShow {
			return helpView(), nil
		}
		return gameengine.Result{}, fmt.Errorf("prod: state bucket unavailable")
	}
	switch a.Verb {
	case VerbShow:
		return show(st, a)
	case VerbOpen:
		return openIncident(tx, st, a)
	case VerbJoin:
		return join(st, a)
	case VerbStart:
		return start(tx, st, a)
	case VerbCancel:
		return cancel(tx, st, a)
	case VerbVote:
		return vote(st, a)
	case VerbClue:
		return clue(st, a)
	case VerbRetro:
		return retrospective(st, a)
	default:
		return gameengine.Result{}, gameengine.Reject("Неизвестное действие инцидента.")
	}
}

func show(st *bolt.Bucket, a gameengine.Action) (gameengine.Result, error) {
	inc, err := currentIncident(st)
	if err != nil {
		return gameengine.Result{}, err
	}
	if inc == nil {
		return helpView(), nil
	}
	r := incidentView(inc, a.Now)
	if inc.Phase == phaseFinal {
		r.Buttons = append(r.Buttons, openButton("Открыть новый инцидент"))
	}
	return r, nil
}

func openIncident(tx *bolt.Tx, st *bolt.Bucket, a gameengine.Action) (gameengine.Result, error) {
	active, err := blockingIncident(st)
	if err != nil {
		return gameengine.Result{}, err
	}
	if active != nil {
		return gameengine.Result{}, gameengine.Reject(fmt.Sprintf(
			"В чате уже идёт инцидент «%s». Дождитесь итога или отмените лобби.", active.Scenario.Title))
	}
	wallet, err := gameengine.Wallet(tx, a.ChatID, a.UserID)
	if err != nil {
		return gameengine.Result{}, err
	}
	if wallet.Score < sponsorMinScore {
		return gameengine.Result{}, gameengine.Reject(fmt.Sprintf(
			"Нужен титул «Дежурный»: %d очков. Сейчас у вас %d. Помогайте чату, чтобы поднять титул.",
			sponsorMinScore, wallet.Score))
	}
	if wallet.Available < SponsorReservation {
		return gameengine.Result{}, gameengine.Reject(fmt.Sprintf(
			"Нужно свободных репутаций: %d. Сейчас доступно %d. Освободите резерв или заработайте репутацию.",
			SponsorReservation, wallet.Available))
	}

	if a.CardID != 0 {
		if a.Arg != strconv.Itoa(SponsorReservation) {
			return gameengine.Result{}, gameengine.Reject("Цена запуска изменилась. Откройте /prod заново.")
		}
		a.Arg = ""
	}
	scen, err := chooseScenario(st, strings.TrimSpace(a.Arg))
	if err != nil {
		return gameengine.Result{}, err
	}
	id, err := nextIncidentID(st)
	if err != nil {
		return gameengine.Result{}, err
	}
	inc := &incident{
		ID:            id,
		Scenario:      scen,
		Phase:         phaseLobby,
		Sponsor:       a.UserID,
		SponsorCost:   SponsorReservation,
		CreatedAt:     a.Now,
		LobbyDeadline: a.Now.Add(LobbyLifetime),
		Votes:         map[int64]string{},
	}
	if err := gameengine.Adjust(tx, a.ChatID, a.UserID, -SponsorReservation, SponsorReservation); err != nil {
		return gameengine.Result{}, err
	}
	if err := appendHistory(st, inc, historyEntry{
		Turn:   0,
		At:     a.Now,
		Actor:  a.UserID,
		Kind:   kindOpen,
		Effect: fmt.Sprintf("лобби открыто; в резерве %d", inc.sponsorCost()),
	}); err != nil {
		return gameengine.Result{}, err
	}
	if err := saveIncident(st, inc); err != nil {
		return gameengine.Result{}, err
	}
	if err := st.Put(keyActive, []byte(inc.ID)); err != nil {
		return gameengine.Result{}, err
	}
	if err := scheduleDeadline(tx, a.ChatID, inc, "lobby", inc.LobbyDeadline); err != nil {
		return gameengine.Result{}, err
	}
	return lobbyView(inc, a.Now), nil
}

func join(st *bolt.Bucket, a gameengine.Action) (gameengine.Result, error) {
	inc, version, err := incidentByArg(st, a.Arg)
	if err != nil {
		return gameengine.Result{}, err
	}
	if inc == nil || inc.Phase != phaseLobby || version != 0 {
		return gameengine.Result{}, staleLobby()
	}
	if !a.Now.Before(inc.LobbyDeadline) {
		return gameengine.Result{}, gameengine.Reject("Лобби уже закрыто. Дежурный откроет новый инцидент.")
	}
	if isParticipant(inc, a.UserID) {
		return gameengine.Result{}, gameengine.Reject("Вы уже участвуете в этом инциденте.")
	}
	inc.Participants = append(inc.Participants, a.UserID)
	if err := appendHistory(st, inc, historyEntry{
		Turn:   0,
		At:     a.Now,
		Actor:  a.UserID,
		Kind:   kindJoin,
		Effect: "присоединился к лобби",
	}); err != nil {
		return gameengine.Result{}, err
	}
	if err := saveIncident(st, inc); err != nil {
		return gameengine.Result{}, err
	}
	return lobbyView(inc, a.Now), nil
}

// LobbyParticipants returns the candidates for a live membership check.
func LobbyParticipants(tx *bolt.Tx, chatID, sponsor int64, arg string) ([]int64, error) {
	st, err := gameengine.State(tx, chatID, ModuleName)
	if err != nil || st == nil {
		return nil, err
	}
	inc, version, err := incidentByArg(st, arg)
	if err != nil || inc == nil || inc.Phase != phaseLobby || version != 0 || inc.Sponsor != sponsor {
		return nil, err
	}
	return uniqueParticipants(inc.Participants), nil
}

func start(tx *bolt.Tx, st *bolt.Bucket, a gameengine.Action) (gameengine.Result, error) {
	inc, version, err := incidentByArg(st, a.Arg)
	if err != nil {
		return gameengine.Result{}, err
	}
	if inc == nil || inc.Phase != phaseLobby || version != 0 {
		return gameengine.Result{}, staleLobby()
	}
	if a.UserID != inc.Sponsor {
		return gameengine.Result{}, gameengine.Reject("Начать инцидент может только дежурный, который открыл лобби.")
	}
	if !a.Now.Before(inc.LobbyDeadline) {
		return gameengine.Result{}, gameengine.Reject("Лобби уже закрыто. Дождитесь сообщения о резерве.")
	}
	if a.Eligible != nil {
		participants := make([]int64, 0, len(a.Eligible))
		for _, id := range uniqueParticipants(a.Eligible) {
			if isParticipant(inc, id) {
				participants = append(participants, id)
			}
		}
		inc.Participants = participants
	}
	if len(uniqueParticipants(inc.Participants)) < 3 {
		return gameengine.Result{}, gameengine.Reject("Нужны три участника. Пригласите ещё одного и нажмите «Начать».")
	}
	if err := gameengine.Adjust(tx, a.ChatID, inc.Sponsor, 0, -inc.sponsorCost()); err != nil {
		return gameengine.Result{}, err
	}
	inc.Phase = phaseRunning
	inc.StartedAt = a.Now
	inc.Turn = 1
	inc.TurnDeadline = a.Now.Add(TurnLifetime)
	inc.Votes = map[int64]string{}
	inc.ClueTurn = 0
	inc.Applied = nil
	inc.TurnResults = nil
	inc.Score = 0
	if err := appendHistory(st, inc, historyEntry{
		Turn:   0,
		At:     a.Now,
		Actor:  a.UserID,
		Kind:   kindStart,
		Effect: fmt.Sprintf("инцидент начат; списано из резерва: %d", inc.sponsorCost()),
	}); err != nil {
		return gameengine.Result{}, err
	}
	if err := saveIncident(st, inc); err != nil {
		return gameengine.Result{}, err
	}
	if err := scheduleDeadline(tx, a.ChatID, inc, "t1", inc.TurnDeadline); err != nil {
		return gameengine.Result{}, err
	}
	return runningView(inc, a.Now), nil
}

func cancel(tx *bolt.Tx, st *bolt.Bucket, a gameengine.Action) (gameengine.Result, error) {
	inc, version, err := incidentByArg(st, a.Arg)
	if err != nil {
		return gameengine.Result{}, err
	}
	if inc == nil || inc.Phase != phaseLobby || version != 0 {
		return gameengine.Result{}, staleLobby()
	}
	if a.UserID != inc.Sponsor {
		return gameengine.Result{}, gameengine.Reject("Отменить лобби может только дежурный, который его открыл.")
	}
	if !a.Now.Before(inc.LobbyDeadline) {
		return gameengine.Result{}, staleLobby()
	}
	if err := gameengine.Adjust(tx, a.ChatID, inc.Sponsor, inc.sponsorCost(), -inc.sponsorCost()); err != nil {
		return gameengine.Result{}, err
	}
	reason := fmt.Sprintf("Дежурный отменил лобби. В кошелёк вернулось %d репы.", inc.sponsorCost())
	if err := appendHistory(st, inc, historyEntry{
		Turn:   0,
		At:     a.Now,
		Actor:  a.UserID,
		Kind:   kindCancel,
		Effect: reason,
	}); err != nil {
		return gameengine.Result{}, err
	}
	finishCancelled(inc, a.Now, reason)
	if err := saveIncident(st, inc); err != nil {
		return gameengine.Result{}, err
	}
	if err := clearActive(st, inc.ID); err != nil {
		return gameengine.Result{}, err
	}
	if err := scheduleDeadline(tx, a.ChatID, inc, "expire", inc.HistoryUntil); err != nil {
		return gameengine.Result{}, err
	}
	return finalView(inc), nil
}

func vote(st *bolt.Bucket, a gameengine.Action) (gameengine.Result, error) {
	inc, turn, optID, err := voteArg(st, a.Arg)
	if err != nil {
		return gameengine.Result{}, err
	}
	if inc == nil || inc.Phase != phaseRunning || turn != inc.Turn {
		return gameengine.Result{}, staleTurn()
	}
	if !a.Now.Before(inc.TurnDeadline) {
		return gameengine.Result{}, staleTurn()
	}
	if !isParticipant(inc, a.UserID) {
		return gameengine.Result{}, gameengine.Reject("Голосовать могут только участники инцидента.")
	}
	opt, ok := optionByID(inc.Scenario.Turns[inc.Turn-1], optID)
	if !ok {
		return gameengine.Result{}, gameengine.Reject("Такого варианта нет. Откройте карточку заново.")
	}
	previousID, hadVote := inc.Votes[a.UserID]
	previousLabel := ""
	effect := "голос принят"
	if hadVote {
		if previousID == opt.ID {
			effect = "голос уже стоял за этот вариант"
		} else {
			effect = "голос изменён"
			if prev, ok := optionByID(inc.Scenario.Turns[inc.Turn-1], previousID); ok {
				previousLabel = prev.Label
			}
		}
	}
	inc.Votes[a.UserID] = opt.ID
	if err := appendHistory(st, inc, historyEntry{
		Turn:          inc.Turn,
		At:            a.Now,
		Actor:         a.UserID,
		Kind:          kindVote,
		OptionID:      opt.ID,
		OptionLabel:   opt.Label,
		PreviousLabel: previousLabel,
		Effect:        effect,
	}); err != nil {
		return gameengine.Result{}, err
	}
	if err := saveIncident(st, inc); err != nil {
		return gameengine.Result{}, err
	}
	return runningView(inc, a.Now), nil
}

func clue(st *bolt.Bucket, a gameengine.Action) (gameengine.Result, error) {
	inc, turn, err := turnArg(st, a.Arg)
	if err != nil {
		return gameengine.Result{}, err
	}
	if inc == nil || inc.Phase != phaseRunning || turn != inc.Turn {
		return gameengine.Result{}, staleTurn()
	}
	if !a.Now.Before(inc.TurnDeadline) {
		return gameengine.Result{}, staleTurn()
	}
	if !isParticipant(inc, a.UserID) {
		return gameengine.Result{}, gameengine.Reject("Подсказку может открыть только участник инцидента.")
	}
	if inc.ClueTurn != 0 {
		return gameengine.Result{}, gameengine.Reject("В этом инциденте подсказку уже использовали.")
	}
	wallet, err := gameengine.Wallet(st.Tx(), a.ChatID, a.UserID)
	if err != nil {
		return gameengine.Result{}, err
	}
	if wallet.Score < reputation.ProdThreshold {
		return gameengine.Result{}, gameengine.Reject("Подсказку может открыть Хранитель прода. Продолжить можно без неё.")
	}
	inc.ClueTurn = inc.Turn
	if err := appendHistory(st, inc, historyEntry{
		Turn:   inc.Turn,
		At:     a.Now,
		Actor:  a.UserID,
		Kind:   kindClue,
		Clue:   inc.Scenario.Turns[inc.Turn-1].Clue,
		Effect: "подсказка раскрыта",
	}); err != nil {
		return gameengine.Result{}, err
	}
	if err := saveIncident(st, inc); err != nil {
		return gameengine.Result{}, err
	}
	return runningView(inc, a.Now), nil
}

func tick(tx *bolt.Tx, a gameengine.Action) (gameengine.Result, error) {
	st, err := gameengine.State(tx, a.ChatID, ModuleName)
	if err != nil {
		var rejection gameengine.Rejection
		if errors.As(err, &rejection) {
			return gameengine.Result{}, nil
		}
		return gameengine.Result{}, err
	}
	if st == nil {
		return gameengine.Result{}, nil
	}
	id, kind, err := tickArg(a.Arg)
	if err != nil {
		return gameengine.Result{}, nil
	}
	inc, err := loadIncident(st, id)
	if err != nil || inc == nil {
		return gameengine.Result{}, err
	}
	switch {
	case kind == "expire":
		c := st.Cursor()
		prefix := historyPrefix(inc.ID)
		for {
			key, _ := c.Seek(prefix)
			if !bytes.HasPrefix(key, prefix) {
				break
			}
			if err := c.Delete(); err != nil {
				return gameengine.Result{}, err
			}
		}
		inc.Participants = nil
		inc.Sponsor = 0
		return gameengine.Result{}, saveIncident(st, inc)
	case kind == "lobby":
		if inc.Phase != phaseLobby {
			return gameengine.Result{}, nil
		}
		if a.Now.Before(inc.LobbyDeadline) {
			return gameengine.Result{}, nil
		}
		if err := gameengine.Adjust(tx, a.ChatID, inc.Sponsor, inc.sponsorCost(), -inc.sponsorCost()); err != nil {
			return gameengine.Result{}, err
		}
		reason := "Трое не набрались. Репа за запуск снова доступна."
		if err := appendHistory(st, inc, historyEntry{
			Turn:   0,
			At:     a.Now,
			Kind:   kindExpire,
			Effect: reason,
		}); err != nil {
			return gameengine.Result{}, err
		}
		finishCancelled(inc, a.Now, reason)
		if err := saveIncident(st, inc); err != nil {
			return gameengine.Result{}, err
		}
		if err := clearActive(st, inc.ID); err != nil {
			return gameengine.Result{}, err
		}
		if err := scheduleDeadline(tx, a.ChatID, inc, "expire", inc.HistoryUntil); err != nil {
			return gameengine.Result{}, err
		}
		return finalView(inc), nil

	case strings.HasPrefix(kind, "t"):
		turn, err := strconv.Atoi(strings.TrimPrefix(kind, "t"))
		if err != nil || turn < 1 || turn > len(inc.Scenario.Turns) {
			return gameengine.Result{}, nil
		}
		if inc.Phase != phaseRunning || inc.Turn != turn {
			return gameengine.Result{}, nil
		}
		if a.Now.Before(inc.TurnDeadline) {
			return gameengine.Result{}, nil
		}
		if err := closeTurn(tx, st, inc, a.ChatID, a.Now); err != nil {
			return gameengine.Result{}, err
		}
		if inc.Phase == phaseFinal {
			return finalView(inc), nil
		}
		return runningView(inc, a.Now), nil
	}
	return gameengine.Result{}, nil
}

func closeTurn(tx *bolt.Tx, st *bolt.Bucket, inc *incident, chatID int64, now time.Time) error {
	turn := inc.Scenario.Turns[inc.Turn-1]
	counts := map[string]int{}
	for _, opt := range turn.Options {
		counts[opt.ID] = 0
	}
	for _, optID := range inc.Votes {
		counts[optID]++
	}
	winner, tied, maxVotes := chooseWinner(turn, counts)
	result := turnResult{Turn: inc.Turn}
	if maxVotes == 0 {
		result.NoVotes = true
		result.Effect = "Голосов не было; ход прошёл без изменений."
	} else {
		delta, effect := winner.effect(inc.Applied)
		result.OptionID = winner.ID
		result.OptionLabel = winner.Label
		result.Counts = countsLine(turn, counts)
		result.Tie = len(tied) > 1
		result.Effect = effect
		result.Delta = delta
		inc.Applied = append(inc.Applied, winner.ID)
		inc.Score += delta
	}
	inc.TurnResults = append(inc.TurnResults, result)
	if err := appendHistory(st, inc, historyEntry{
		Turn:        inc.Turn,
		At:          now,
		Kind:        kindResult,
		OptionID:    result.OptionID,
		OptionLabel: result.OptionLabel,
		Effect:      result.Effect,
		Counts:      result.Counts,
		Tie:         result.Tie,
	}); err != nil {
		return err
	}
	inc.Votes = map[int64]string{}
	if inc.Turn >= len(inc.Scenario.Turns) {
		finishRound(inc, now)
		if err := saveIncident(st, inc); err != nil {
			return err
		}
		if err := scheduleDeadline(tx, chatID, inc, "expire", inc.HistoryUntil); err != nil {
			return err
		}
		return clearActive(st, inc.ID)
	}
	inc.Turn++
	inc.TurnDeadline = now.Add(TurnLifetime)
	if err := saveIncident(st, inc); err != nil {
		return err
	}
	return scheduleDeadline(tx, chatID, inc, fmt.Sprintf("t%d", inc.Turn), inc.TurnDeadline)
}

func chooseWinner(turn turn, counts map[string]int) (option, []option, int) {
	maxVotes := 0
	for _, opt := range turn.Options {
		if counts[opt.ID] > maxVotes {
			maxVotes = counts[opt.ID]
		}
	}
	if maxVotes == 0 {
		return option{}, nil, 0
	}
	var tied []option
	for _, opt := range turn.Options {
		if counts[opt.ID] == maxVotes {
			tied = append(tied, opt)
		}
	}
	if len(tied) > 1 {
		for _, opt := range tied {
			if opt.Conservative {
				return opt, tied, maxVotes
			}
		}
	}
	return tied[0], tied, maxVotes
}

func countsLine(turn turn, counts map[string]int) string {
	parts := make([]string, 0, len(turn.Options))
	for _, opt := range turn.Options {
		parts = append(parts, strconv.Itoa(counts[opt.ID]))
	}
	return strings.Join(parts, ":")
}

func finishRound(inc *incident, now time.Time) {
	inc.Phase = phaseFinal
	inc.FinishedAt = now
	inc.HistoryUntil = now.Add(HistoryRetention)
	inc.Outcome = outcomeUnresolved
	if inc.Score >= inc.Scenario.RestoreAt && len(inc.TurnResults) > 0 {
		last := inc.TurnResults[len(inc.TurnResults)-1]
		if !last.NoVotes && contains(inc.Scenario.FinalOK, last.OptionID) {
			inc.Outcome = outcomeRestored
		}
	}
}

func finishCancelled(inc *incident, now time.Time, reason string) {
	inc.Phase = phaseFinal
	inc.Outcome = outcomeCancelled
	inc.CancelReason = reason
	inc.FinishedAt = now
	inc.HistoryUntil = now.Add(HistoryRetention)
}

func scheduleDeadline(tx *bolt.Tx, chatID int64, inc *incident, kind string, at time.Time) error {
	key := fmt.Sprintf("%s:%d:%s:%s", ModuleName, chatID, inc.ID, kind)
	return gameengine.Schedule(tx, ModuleName, gameengine.Action{
		ChatID: chatID,
		Key:    key,
		Verb:   VerbTick,
		Arg:    inc.ID + ":" + kind,
		Now:    at.UTC(),
	})
}

func appendHistory(st *bolt.Bucket, inc *incident, e historyEntry) error {
	inc.HistorySeq++
	e.Seq = inc.HistorySeq
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return st.Put(historyKey(inc.ID, e.Seq), data)
}

func historyKey(id string, seq int64) []byte {
	return []byte(fmt.Sprintf("h:%s:%020d", id, seq))
}

func historyPrefix(id string) []byte {
	return []byte("h:" + id + ":")
}

func incidentKey(id string) []byte {
	return []byte("i:" + id)
}

func loadHistory(st *bolt.Bucket, id string) ([]historyEntry, error) {
	prefix := historyPrefix(id)
	var out []historyEntry
	c := st.Cursor()
	for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
		var e historyEntry
		if err := json.Unmarshal(v, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func loadIncident(st *bolt.Bucket, id string) (*incident, error) {
	data := st.Get(incidentKey(id))
	if data == nil {
		return nil, nil
	}
	var inc incident
	if err := json.Unmarshal(data, &inc); err != nil {
		return nil, err
	}
	return &inc, nil
}

func saveIncident(st *bolt.Bucket, inc *incident) error {
	data, err := json.Marshal(inc)
	if err != nil {
		return err
	}
	return st.Put(incidentKey(inc.ID), data)
}

func blockingIncident(st *bolt.Bucket) (*incident, error) {
	inc, err := currentIncident(st)
	if err != nil || inc == nil {
		return nil, err
	}
	if inc.Phase == phaseLobby || inc.Phase == phaseRunning {
		return inc, nil
	}
	return nil, nil
}

func currentIncident(st *bolt.Bucket) (*incident, error) {
	id := string(st.Get(keyActive))
	if id == "" {
		id = string(st.Get(keyLast))
	}
	if id == "" {
		return nil, nil
	}
	return loadIncident(st, id)
}

func clearActive(st *bolt.Bucket, id string) error {
	if string(st.Get(keyActive)) != id {
		return nil
	}
	if err := st.Delete(keyActive); err != nil {
		return err
	}
	return st.Put(keyLast, []byte(id))
}

func nextIncidentID(st *bolt.Bucket) (string, error) {
	var n int64
	if raw := st.Get(keySeq); raw != nil {
		parsed, err := strconv.ParseInt(string(raw), 10, 64)
		if err != nil {
			return "", err
		}
		n = parsed
	}
	n++
	if err := st.Put(keySeq, []byte(strconv.FormatInt(n, 10))); err != nil {
		return "", err
	}
	return strconv.FormatInt(n, 10), nil
}

func chooseScenario(st *bolt.Bucket, requested string) (scenario, error) {
	if requested != "" {
		scen, ok := scenarioByID(requested)
		if !ok {
			return scenario{}, gameengine.Reject("Такого сценария нет. Доступны: search, queue, certs.")
		}
		return scen, nil
	}
	var rotation int
	if raw := st.Get(keyRotation); raw != nil {
		parsed, err := strconv.Atoi(string(raw))
		if err != nil {
			return scenario{}, err
		}
		rotation = parsed
	}
	id := scenarioIDs[rotation%len(scenarioIDs)]
	rotation++
	if err := st.Put(keyRotation, []byte(strconv.Itoa(rotation))); err != nil {
		return scenario{}, err
	}
	scen, _ := scenarioByID(id)
	return scen, nil
}

func incidentByArg(st *bolt.Bucket, arg string) (*incident, int, error) {
	parts := strings.Split(arg, ":")
	if len(parts) < 2 || parts[0] == "" {
		return nil, 0, staleLobby()
	}
	version, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, 0, staleLobby()
	}
	inc, err := loadIncident(st, parts[0])
	if err != nil {
		return nil, 0, err
	}
	return inc, version, nil
}

func voteArg(st *bolt.Bucket, arg string) (*incident, int, string, error) {
	parts := strings.Split(arg, ":")
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
		return nil, 0, "", staleTurn()
	}
	turn, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, 0, "", staleTurn()
	}
	inc, err := loadIncident(st, parts[0])
	if err != nil {
		return nil, 0, "", err
	}
	return inc, turn, parts[2], nil
}

func turnArg(st *bolt.Bucket, arg string) (*incident, int, error) {
	parts := strings.Split(arg, ":")
	if len(parts) != 2 || parts[0] == "" {
		return nil, 0, staleTurn()
	}
	turn, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, 0, staleTurn()
	}
	inc, err := loadIncident(st, parts[0])
	if err != nil {
		return nil, 0, err
	}
	return inc, turn, nil
}

func tickArg(arg string) (string, string, error) {
	parts := strings.Split(arg, ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("prod: malformed tick arg")
	}
	return parts[0], parts[1], nil
}

func isParticipant(inc *incident, userID int64) bool {
	for _, id := range inc.Participants {
		if id == userID {
			return true
		}
	}
	return false
}

func uniqueParticipants(ids []int64) []int64 {
	seen := make(map[int64]bool, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func optionByID(turn turn, id string) (option, bool) {
	for _, opt := range turn.Options {
		if opt.ID == id {
			return opt, true
		}
	}
	return option{}, false
}

func staleLobby() error {
	return gameengine.Reject("Лобби уже закрыто. Откройте карточку заново.")
}

func staleTurn() error {
	return gameengine.Reject("Этот ход уже закрыт. Откройте карточку, чтобы посмотреть итог.")
}
