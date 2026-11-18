package prod

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/gameengine"
)

func newHarness(t *testing.T) (*gameengine.Engine, *bolt.DB) {
	t.Helper()
	db, err := bolt.Open(filepath.Join(t.TempDir(), "prod.db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return gameengine.New(db, map[string]gameengine.Module{ModuleName: Apply}), db
}

func fund(t *testing.T, db *bolt.DB, chatID, userID int64, amount int) {
	t.Helper()
	if err := db.Update(func(tx *bolt.Tx) error {
		return gameengine.Adjust(tx, chatID, userID, amount, 0)
	}); err != nil {
		t.Fatal(err)
	}
}

func applyAction(t *testing.T, engine *gameengine.Engine, a gameengine.Action) gameengine.Result {
	t.Helper()
	result, err := engine.Apply(ModuleName, a)
	if err != nil {
		t.Fatalf("%s %s: %v", a.Verb, a.Arg, err)
	}
	return result
}

func buttonWithVerb(t *testing.T, result gameengine.Result, verb string) gameengine.Button {
	t.Helper()
	for _, button := range result.Buttons {
		if button.Verb == verb {
			return button
		}
	}
	t.Fatalf("no %q button in %q", verb, result.Text)
	return gameengine.Button{}
}

func wallet(t *testing.T, db *bolt.DB, chatID, userID int64) (available, reserved, score int) {
	t.Helper()
	if err := db.View(func(tx *bolt.Tx) error {
		account, err := gameengine.Wallet(tx, chatID, userID)
		if err != nil {
			return err
		}
		available, reserved, score = account.Available, account.Reserved, account.Score
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return available, reserved, score
}

func TestIncidentRoundAndRetrospective(t *testing.T) {
	previous := sponsorMinScore
	sponsorMinScore = 0
	t.Cleanup(func() { sponsorMinScore = previous })

	engine, db := newHarness(t)
	const chatID int64 = 700
	const sponsor int64 = 1
	fund(t, db, chatID, sponsor, 20)
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	opened := applyAction(t, engine, gameengine.Action{
		ChatID: chatID, UserID: sponsor, Key: "open", Verb: VerbOpen, Arg: "search", Now: now,
	})
	if !strings.Contains(opened.Text, "лобби") && !strings.Contains(opened.Text, "Участники") {
		t.Fatalf("lobby card: %q", opened.Text)
	}
	joinButton := buttonWithVerb(t, opened, VerbJoin)
	id := strings.Split(joinButton.Arg, ":")[0]
	if joinButton.Arg != id+":0" {
		t.Fatalf("join arg lacks version: %q", joinButton.Arg)
	}
	if available, reserved, _ := wallet(t, db, chatID, sponsor); available != 12 || reserved != 18 {
		t.Fatalf("open wallet = %d/%d, want 12/18", available, reserved)
	}

	for i, userID := range []int64{1, 2, 3} {
		at := now.Add(time.Duration(5+i) * time.Second)
		joined := applyAction(t, engine, gameengine.Action{
			ChatID: chatID, UserID: userID, Key: "join-" + id, Verb: VerbJoin, Arg: id + ":0", Now: at,
		})
		if !strings.Contains(joined.Text, "Для старта нужно три участника") && i < 2 {
			t.Fatalf("join %d card: %q", i, joined.Text)
		}
	}

	started := applyAction(t, engine, gameengine.Action{
		ChatID: chatID, UserID: sponsor, Key: "start", Verb: VerbStart, Arg: id + ":0", Now: now.Add(20 * time.Second),
	})
	if !strings.Contains(started.Text, "Смена 1 из 3") {
		t.Fatalf("start card: %q", started.Text)
	}
	if available, reserved, _ := wallet(t, db, chatID, sponsor); available != 12 || reserved != 0 {
		t.Fatalf("start wallet = %d/%d, want 12/0", available, reserved)
	}

	vote := func(userID int64, key, option string, at time.Time) gameengine.Result {
		return applyAction(t, engine, gameengine.Action{
			ChatID: chatID, UserID: userID, Key: key, Verb: VerbVote,
			Arg: id + ":1:" + option, Now: at,
		})
	}
	vote(1, "v1", "t1rollback", now.Add(30*time.Second))
	vote(2, "v2", "t1rollback", now.Add(31*time.Second))
	vote(3, "v3", "t1cache", now.Add(32*time.Second))
	vote(3, "v3-changed", "t1rollback", now.Add(33*time.Second))

	settled, err := engine.Advance(now.Add(65 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(settled) != 1 || !strings.Contains(settled[0].Result.Text, "Смена 2 из 3") {
		t.Fatalf("turn 1 settlement: %+v", settled)
	}

	steps := []struct {
		turn    int
		option  string
		voteAt  time.Time
		closeAt time.Time
	}{
		{turn: 2, option: "t2reindex", voteAt: now.Add(90 * time.Second), closeAt: now.Add(110 * time.Second)},
		{turn: 3, option: "t3verify", voteAt: now.Add(130 * time.Second), closeAt: now.Add(155 * time.Second)},
	}
	for _, step := range steps {
		for userID := int64(1); userID <= 3; userID++ {
			applyAction(t, engine, gameengine.Action{
				ChatID: chatID, UserID: userID,
				Key:  fmt.Sprintf("turn%d-%d", step.turn, userID),
				Verb: VerbVote,
				Arg:  fmt.Sprintf("%s:%d:%s", id, step.turn, step.option),
				Now:  step.voteAt,
			})
		}
		settled, err = engine.Advance(step.closeAt)
		if err != nil {
			t.Fatal(err)
		}
		if len(settled) != 1 {
			t.Fatalf("turn %d settlement count: %+v", step.turn, settled)
		}
		if step.turn == 2 && !strings.Contains(settled[0].Result.Text, "Смена 3 из 3") {
			t.Fatalf("turn 2 settlement: %q", settled[0].Result.Text)
		}
	}
	final := settled[0].Result
	if !strings.Contains(final.Text, "сервис восстановлен") {
		t.Fatalf("final card: %q", final.Text)
	}
	if !strings.Contains(final.Text, "Причина:") || !strings.Contains(final.Text, "Что повлияло:") {
		t.Fatalf("final card lacks cause/debrief: %q", final.Text)
	}
	retroButton := buttonWithVerb(t, final, VerbRetro)
	if retroButton.Arg != id+":3:1" {
		t.Fatalf("retro arg = %q, want version and page", retroButton.Arg)
	}

	retro := applyAction(t, engine, gameengine.Action{
		ChatID: chatID, UserID: 2, Key: "retro-1", Verb: VerbRetro, Arg: retroButton.Arg, Now: now.Add(160 * time.Second),
	})
	if !strings.Contains(retro.Text, "Разбор инцидента") || !strings.Contains(retro.Text, "Ход 1") {
		t.Fatalf("retro page 1: %q", retro.Text)
	}
	next := buttonWithVerb(t, retro, VerbRetro)
	if !strings.HasSuffix(next.Arg, ":2") {
		t.Fatalf("retro forward arg = %q", next.Arg)
	}
	retro2 := applyAction(t, engine, gameengine.Action{
		ChatID: chatID, UserID: 2, Key: "retro-2", Verb: VerbRetro, Arg: next.Arg, Now: now.Add(161 * time.Second),
	})
	allRetro := retro.Text + retro2.Text
	if !strings.Contains(allRetro, "«Очистить кэш»") || !strings.Contains(allRetro, "изменён с") {
		t.Fatalf("retro hides a changed, losing vote: %q", allRetro)
	}
	if !strings.Contains(allRetro, "Выполнили: «Откатить выкладку»") {
		t.Fatalf("retro lacks applied turn result: %q", allRetro)
	}

	expired := now.Add(160*time.Second + 31*24*time.Hour)
	if _, err := engine.Advance(expired); err != nil {
		t.Fatal(err)
	}
	_, err = engine.Apply(ModuleName, gameengine.Action{
		ChatID: chatID, UserID: 2, Key: "retro-1", Verb: VerbRetro,
		Arg: retroButton.Arg, Now: expired,
	})
	if err == nil || err.Error() != "Разбор больше не хранится." {
		t.Fatalf("expired retro error = %v", err)
	}
	if err := db.View(func(tx *bolt.Tx) error {
		st, err := gameengine.State(tx, chatID, ModuleName)
		if err != nil {
			return err
		}
		entries, err := loadHistory(st, id)
		if len(entries) != 0 {
			t.Errorf("expired incident kept %d personal history entries", len(entries))
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLobbyExpiryReleasesReservation(t *testing.T) {
	previous := sponsorMinScore
	sponsorMinScore = 0
	t.Cleanup(func() { sponsorMinScore = previous })

	engine, db := newHarness(t)
	const chatID int64 = 800
	const sponsor int64 = 9
	fund(t, db, chatID, sponsor, 10)
	now := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

	applyAction(t, engine, gameengine.Action{
		ChatID: chatID, UserID: sponsor, Key: "open", Verb: VerbOpen, Arg: "queue", Now: now,
	})
	if available, reserved, _ := wallet(t, db, chatID, sponsor); available != 2 || reserved != 18 {
		t.Fatalf("open wallet = %d/%d, want 2/18", available, reserved)
	}

	settled, err := engine.Advance(now.Add(LobbyLifetime))
	if err != nil {
		t.Fatal(err)
	}
	if len(settled) != 1 || !strings.Contains(settled[0].Result.Text, "Трое не набрались") {
		t.Fatalf("lobby expiry settlement: %+v", settled)
	}
	if available, reserved, _ := wallet(t, db, chatID, sponsor); available != 20 || reserved != 0 {
		t.Fatalf("expired wallet = %d/%d, want 20/0", available, reserved)
	}
}

func TestSponsorNeedsTitle(t *testing.T) {
	previous := sponsorMinScore
	sponsorMinScore = SponsorScore
	t.Cleanup(func() { sponsorMinScore = previous })

	engine, db := newHarness(t)
	const chatID int64 = 900
	fund(t, db, chatID, 4, 10)
	_, err := engine.Apply(ModuleName, gameengine.Action{
		ChatID: chatID, UserID: 4, Key: "open", Verb: VerbOpen, Arg: "certs",
		Now: time.Date(2026, 3, 3, 9, 0, 0, 0, time.UTC),
	})
	if err == nil || !strings.Contains(err.Error(), "Дежурный") {
		t.Fatalf("sponsor refusal = %v", err)
	}
}

func TestChooseWinnerTieRules(t *testing.T) {
	turn := turn{Options: []option{
		{ID: "first"},
		{ID: "conservative", Conservative: true},
		{ID: "third"},
	}}
	winner, tied, maxVotes := chooseWinner(turn, map[string]int{"first": 2, "conservative": 2, "third": 1})
	if winner.ID != "conservative" || len(tied) != 2 || maxVotes != 2 {
		t.Fatalf("conservative tie = %+v / %d", winner, len(tied))
	}
	winner, tied, _ = chooseWinner(turn, map[string]int{"first": 2, "conservative": 1, "third": 2})
	if winner.ID != "first" || len(tied) != 2 {
		t.Fatalf("published order tie = %+v / %d", winner, len(tied))
	}
	winner, _, maxVotes = chooseWinner(turn, map[string]int{})
	if winner.ID != "" || maxVotes != 0 {
		t.Fatalf("empty tally = %+v / %d", winner, maxVotes)
	}
}

func TestAuthoredScenariosHaveWinningSequence(t *testing.T) {
	sequences := map[string][]string{
		"search": {"t1rollback", "t2reindex", "t3verify"},
		"queue":  {"t1pause", "t2unlock", "t3intake"},
		"certs":  {"t1rollback", "t2chain", "t3handshake"},
	}
	for id, sequence := range sequences {
		scen, ok := scenarioByID(id)
		if !ok {
			t.Fatalf("scenario %q missing", id)
		}
		score := 0
		applied := []string{}
		for i, optionID := range sequence {
			opt, ok := optionByID(scen.Turns[i], optionID)
			if !ok {
				t.Fatalf("%s turn %d option %q missing", id, i+1, optionID)
			}
			delta, _ := opt.effect(applied)
			score += delta
			applied = append(applied, optionID)
		}
		if score < scen.RestoreAt || !contains(scen.FinalOK, sequence[len(sequence)-1]) {
			t.Fatalf("%s winning sequence scores %d, needs %d and final %v", id, score, scen.RestoreAt, scen.FinalOK)
		}
	}
}
