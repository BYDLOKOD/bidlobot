package automat

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/gameengine"
)

func testDB(t *testing.T) *bolt.DB {
	t.Helper()
	db, err := bolt.Open(filepath.Join(t.TempDir(), "automat.db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func apply(t *testing.T, db *bolt.DB, a gameengine.Action) (gameengine.Result, error) {
	t.Helper()
	var res gameengine.Result
	err := db.Update(func(tx *bolt.Tx) error {
		var err error
		res, err = Apply(tx, a)
		return err
	})
	return res, err
}

func seed(t *testing.T, db *bolt.DB, chatID, userID int64, amount int) {
	t.Helper()
	err := db.Update(func(tx *bolt.Tx) error {
		return gameengine.Adjust(tx, chatID, userID, amount, 0)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func available(t *testing.T, db *bolt.DB, chatID, userID int64) int {
	t.Helper()
	var balance int
	err := db.Update(func(tx *bolt.Tx) error {
		w, err := gameengine.Wallet(tx, chatID, userID)
		balance = w.Available
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return balance
}

func deterministicPick(t *testing.T) {
	t.Helper()
	previous := pickIndex
	pickIndex = func(n int) (int, error) {
		if n <= 0 {
			return 0, fmt.Errorf("empty pick")
		}
		return 0, nil
	}
	t.Cleanup(func() { pickIndex = previous })
}

func confirmArg(t *testing.T, res gameengine.Result) string {
	t.Helper()
	for _, b := range res.Buttons {
		if b.Verb == "confirm" {
			return b.Arg
		}
	}
	t.Fatalf("no confirm button in %+v", res)
	return ""
}

// spinOnce runs the prompt and the confirmation and returns the spin result.
func spinOnce(t *testing.T, db *bolt.DB, chatID, userID int64, key string, now time.Time) (gameengine.Result, error) {
	t.Helper()
	prompt, err := apply(t, db, gameengine.Action{ChatID: chatID, UserID: userID, Key: key + ":s", Verb: "spin", Now: now})
	if err != nil {
		return gameengine.Result{}, err
	}
	return apply(t, db, gameengine.Action{
		ChatID: chatID, UserID: userID, Key: key + ":c",
		Verb: "confirm", Arg: confirmArg(t, prompt), Now: now,
	})
}

// arm gives a member one automatic reply: a spun phrase, equipped and enabled.
func arm(t *testing.T, db *bolt.DB, chatID, userID int64, now time.Time) {
	t.Helper()
	seed(t, db, chatID, userID, 20)
	if _, err := spinOnce(t, db, chatID, userID, fmt.Sprintf("arm-%d", userID), now); err != nil {
		t.Fatal(err)
	}
	settings, err := lookup(t, db, chatID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.Owned) != 1 {
		t.Fatalf("owned after spin: %+v", settings.Owned)
	}
	if _, err := apply(t, db, gameengine.Action{
		ChatID: chatID, UserID: userID, Key: fmt.Sprintf("equip-%d", userID),
		Verb: "equip", Arg: fmt.Sprint(settings.Owned[0].ID), Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := apply(t, db, gameengine.Action{
		ChatID: chatID, UserID: userID, Key: fmt.Sprintf("enable-%d", userID),
		Verb: "enable", Now: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func lookup(t *testing.T, db *bolt.DB, chatID, userID int64) (Settings, error) {
	t.Helper()
	var s Settings
	err := db.View(func(tx *bolt.Tx) error {
		var err error
		s, err = Lookup(tx, chatID, userID)
		return err
	})
	return s, err
}

func TestCatalogueMatchesTZ(t *testing.T) {
	want := []string{
		`У меня локально работает.`,
		`Я думал, это черновик.`,
		`Это согласовали на созвоне, записи нет.`,
		`Я как раз собирался это проверить.`,
		`В документации был другой понедельник.`,
		`Я нажал "сохранить". Дальше началась самостоятельная жизнь файла.`,
		`У меня открыта правильная вкладка. Просто не та.`,
		`Я не опоздал. Я пришёл к повторному обсуждению.`,
		`Сначала надо выяснить, кто записался добровольцем. Надеюсь, не я.`,
		`Я отвечу, как только вспомню вопрос.`,
		`Последний раз я видел это в рабочем состоянии.`,
		`Я оставил это до утра. Утро пришло без предупреждения.`,
	}
	got := Catalogue()
	if len(got) != len(want) {
		t.Fatalf("catalogue size = %d, want %d", len(got), len(want))
	}
	seen := map[string]bool{}
	for i, it := range got {
		if it.ID != i+1 {
			t.Errorf("item %d has id %d", i, it.ID)
		}
		if it.Phrase != want[i] {
			t.Errorf("item %d = %q, want %q", i+1, it.Phrase, want[i])
		}
		if seen[it.Phrase] {
			t.Errorf("duplicate phrase %q", it.Phrase)
		}
		seen[it.Phrase] = true
		for _, bad := range []string{"<", ">", "&"} {
			if strings.Contains(it.Phrase, bad) {
				t.Errorf("phrase %d contains %q unsafe for Telegram HTML", i+1, bad)
			}
		}
	}
}

func TestViewLeadsWithLeverAndSeparatesCollection(t *testing.T) {
	db := testDB(t)
	res, err := apply(t, db, gameengine.Action{ChatID: 5, UserID: 7, Key: "view-1", Verb: "view", Now: time.Unix(1770000000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"Автомат отмазок", "Без повторов", "8 репутаций"} {
		if !strings.Contains(res.Text, part) {
			t.Errorf("view text %q missing %q", res.Text, part)
		}
	}
	if len(res.Buttons) != 2 || res.Buttons[0].Verb != "spin" || res.Buttons[1].Verb != "collection" {
		t.Fatalf("main card should offer lever and collection: %+v", res.Buttons)
	}
	empty, err := apply(t, db, gameengine.Action{ChatID: 5, UserID: 7, Key: "empty", Verb: "collection"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(empty.Text, "Пока нет отмазок") || len(empty.Buttons) != 1 || empty.Buttons[0].Verb != "view" {
		t.Fatalf("empty collection: %+v", empty)
	}
}

func TestSpinResultAndCollectionDoNotEnableReplies(t *testing.T) {
	deterministicPick(t)
	db := testDB(t)
	const chat, user = int64(15), int64(16)
	now := time.Unix(1770000000, 0)
	seed(t, db, chat, user, 20)
	result, err := spinOnce(t, db, chat, user, "first", now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.Text, catalogue[0].Phrase+"\n") || len(result.Buttons) != 3 ||
		result.Buttons[0].Verb != "spin" || result.Buttons[1].Verb != "equip" || result.Buttons[1].Arg != "1" ||
		result.Buttons[2].Verb != "collection" {
		t.Fatalf("spin result: %+v", result)
	}
	balance := available(t, db, chat, user)
	for _, verb := range []string{"view", "collection"} {
		res, err := apply(t, db, gameengine.Action{ChatID: chat, UserID: user, Verb: verb})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(res.Text, catalogue[0].Phrase) != (verb == "collection") {
			t.Fatalf("%s: %+v", verb, res)
		}
	}
	settings, err := lookup(t, db, chat, user)
	if err != nil || settings.Enabled || settings.Equipped.ID != 0 || len(settings.Owned) != 1 ||
		settings.Owned[0].ID != 1 || available(t, db, chat, user) != balance {
		t.Fatalf("navigation altered state: %+v, %v", settings, err)
	}
}

func TestSpinDebitsEightAndReturnsUniquePhrases(t *testing.T) {
	deterministicPick(t)
	db := testDB(t)
	const chat, user = int64(11), int64(22)
	now := time.Unix(1770000000, 0)
	seed(t, db, chat, user, 100)
	start := available(t, db, chat, user)

	seen := map[int]bool{}
	for i := range len(catalogue) {
		res, err := spinOnce(t, db, chat, user, fmt.Sprintf("spin-%d", i), now)
		if err != nil {
			t.Fatalf("spin %d: %v", i, err)
		}
		if !strings.Contains(res.Text, "Списано 8 репутаций") || !strings.Contains(res.Text, "В коллекции") {
			t.Errorf("spin %d text: %q", i, res.Text)
		}
		settings, err := lookup(t, db, chat, user)
		if err != nil {
			t.Fatal(err)
		}
		if len(settings.Owned) != i+1 {
			t.Fatalf("owned after spin %d = %d", i, len(settings.Owned))
		}
		newest := settings.Owned[len(settings.Owned)-1]
		if seen[newest.ID] {
			t.Fatalf("duplicate award %d", newest.ID)
		}
		seen[newest.ID] = true
	}
	if len(seen) != len(catalogue) {
		t.Fatalf("awarded %d distinct items", len(seen))
	}
	after := available(t, db, chat, user)
	if after != start-len(catalogue)*8 {
		t.Fatalf("balance after full collection = %d, want %d", after, start-len(catalogue)*8)
	}

	if _, err := apply(t, db, gameengine.Action{ChatID: chat, UserID: user, Key: "spin-complete", Verb: "spin", Now: now}); err == nil ||
		!strings.Contains(err.Error(), "Коллекция собрана") {
		t.Fatalf("complete prompt err = %v", err)
	}
	if _, err := apply(t, db, gameengine.Action{
		ChatID: chat, UserID: user, Key: "confirm-complete",
		Verb: "confirm", Arg: spinQuote(fullMask()), Now: now,
	}); err == nil || !strings.Contains(err.Error(), "Репутация не списана") {
		t.Fatalf("complete confirm err = %v", err)
	}
	if untouched := available(t, db, chat, user); untouched != after {
		t.Fatalf("balance changed on declined spin: %d -> %d", after, untouched)
	}
}

func TestSpinRejectsInsufficientFunds(t *testing.T) {
	deterministicPick(t)
	db := testDB(t)
	const chat, user = int64(31), int64(32)
	now := time.Unix(1770000000, 0)
	for i := range 1 {
		if _, err := spinOnce(t, db, chat, user, fmt.Sprintf("poor-%d", i), now); err != nil {
			t.Fatalf("spin %d: %v", i, err)
		}
	}
	if got := available(t, db, chat, user); got != 2 {
		t.Fatalf("available = %d, want 2", got)
	}
	settings, err := lookup(t, db, chat, user)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apply(t, db, gameengine.Action{
		ChatID: chat, UserID: user, Key: "broke-confirm", Verb: "confirm",
		Arg: spinQuote(settingsMask(settings)), Now: now,
	}); err == nil || !strings.Contains(err.Error(), "Не хватает репутации") {
		t.Fatalf("confirm err = %v", err)
	}
	if still := available(t, db, chat, user); still != 2 {
		t.Fatalf("balance changed on rejected spin: %d", still)
	}
	if _, err := apply(t, db, gameengine.Action{ChatID: chat, UserID: user, Key: "broke-spin", Verb: "spin", Now: now}); err == nil ||
		!strings.Contains(err.Error(), "Не хватает репутации") {
		t.Fatalf("prompt err = %v", err)
	}
}

func settingsMask(s Settings) uint32 {
	var mask uint32
	for _, it := range s.Owned {
		mask |= 1 << uint(it.ID-1)
	}
	return mask
}

func TestConfirmRejectsStaleCollectionVersion(t *testing.T) {
	db := testDB(t)
	now := time.Unix(1770000000, 0)
	seed(t, db, 41, 42, 20)
	if _, err := apply(t, db, gameengine.Action{
		ChatID: 41, UserID: 42, Key: "stale-confirm", Verb: "confirm", Arg: spinQuote(13), Now: now,
	}); err == nil || !strings.Contains(err.Error(), "Коллекция изменилась") {
		t.Fatalf("stale confirm err = %v", err)
	}
	if _, err := apply(t, db, gameengine.Action{
		ChatID: 41, UserID: 42, Verb: "confirm", Arg: "0", Now: now,
	}); err == nil || !strings.Contains(err.Error(), "ключ") {
		t.Fatalf("missing key err = %v", err)
	}
}

func TestEquipEnableDisable(t *testing.T) {
	deterministicPick(t)
	db := testDB(t)
	const chat, user = int64(51), int64(52)
	now := time.Unix(1770000000, 0)
	seed(t, db, chat, user, 20)
	if _, err := spinOnce(t, db, chat, user, "eq", now); err != nil {
		t.Fatal(err)
	}
	if _, err := apply(t, db, gameengine.Action{ChatID: chat, UserID: user, Key: "enable-empty", Verb: "enable", Now: now}); err == nil ||
		!strings.Contains(err.Error(), "Сначала выберите") {
		t.Fatalf("enable without phrase err = %v", err)
	}
	res, err := apply(t, db, gameengine.Action{ChatID: chat, UserID: user, Key: "equip", Verb: "equip", Arg: "1", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "Для автоответа выбрана фраза:") || !strings.Contains(res.Text, "Автоответ: выключен") {
		t.Fatalf("equip text = %q", res.Text)
	}
	s, err := lookup(t, db, chat, user)
	if err != nil {
		t.Fatal(err)
	}
	if s.Equipped.ID != 1 || s.Enabled {
		t.Fatalf("settings after equip: %+v", s)
	}
	if !strings.Contains(res.Text, "У меня локально работает.") {
		t.Fatalf("view does not show selected phrase: %q", res.Text)
	}
	res, err = apply(t, db, gameengine.Action{ChatID: chat, UserID: user, Key: "enable", Verb: "enable", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "Автоответ: включён") {
		t.Fatalf("enable text = %q", res.Text)
	}
	res, err = apply(t, db, gameengine.Action{ChatID: chat, UserID: user, Key: "disable", Verb: "disable", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "Автоответ: выключен") {
		t.Fatalf("disable text = %q", res.Text)
	}
	if _, err := apply(t, db, gameengine.Action{ChatID: chat, UserID: user, Key: "equip-missing", Verb: "equip", Arg: "2", Now: now}); err == nil ||
		!strings.Contains(err.Error(), "ещё не выпадала") {
		t.Fatalf("equip unowned err = %v", err)
	}
}

func TestCollectionsAreChatScoped(t *testing.T) {
	deterministicPick(t)
	db := testDB(t)
	now := time.Unix(1770000000, 0)
	seed(t, db, 61, 62, 20)
	if _, err := spinOnce(t, db, 61, 62, "scope", now); err != nil {
		t.Fatal(err)
	}
	other, err := lookup(t, db, 63, 62)
	if err != nil {
		t.Fatal(err)
	}
	if len(other.Owned) != 0 {
		t.Fatalf("other chat inherited collection: %+v", other.Owned)
	}
}

func TestReserveReplyCapsAndDedup(t *testing.T) {
	deterministicPick(t)
	db := testDB(t)
	const chat, targetB, targetC = int64(71), int64(72), int64(73)
	t0 := time.Unix(1770000000, 0)
	arm(t, db, chat, targetB, t0)
	arm(t, db, chat, targetC, t0)
	arm(t, db, 999, targetB, t0)

	type step struct {
		name   string
		owner  int64
		msg    int64
		at     time.Duration
		want   bool
		chatID int64
	}
	steps := []step{
		{"first", targetB, 100, 0, true, chat},
		{"duplicate original message", targetB, 100, time.Hour, false, chat},
		{"chat cooldown", targetC, 101, 5 * time.Second, false, chat},
		{"target cooldown wins after chat window", targetB, 102, 31 * time.Second, false, chat},
		{"other target after chat window", targetC, 103, 31 * time.Second, true, chat},
		{"target cooldown after chat window", targetB, 106, 62 * time.Second, false, chat},
		{"target window elapsed", targetB, 105, 10*time.Minute + 2*time.Second, true, chat},
		{"duplicate of second reply", targetC, 103, time.Hour, false, chat},
		{"same message id in other chat", targetB, 100, time.Hour, true, 999},
	}
	for _, s := range steps {
		var got *PendingReply
		err := db.Update(func(tx *bolt.Tx) error {
			var err error
			got, err = ReserveReply(tx, s.chatID, s.owner, s.msg, t0.Add(s.at))
			return err
		})
		if err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if (got != nil) != s.want {
			t.Fatalf("%s: reserved=%v want %v", s.name, got != nil, s.want)
		}
		if got != nil {
			if got.OwnerID != s.owner || got.SourceMessageID != s.msg || got.PhraseID < 1 || got.Phrase == "" {
				t.Fatalf("%s: bad reservation %+v", s.name, got)
			}
		}
	}
}

func TestReserveReplySkipsDisabledAndUnowned(t *testing.T) {
	deterministicPick(t)
	db := testDB(t)
	const chat = int64(81)
	t0 := time.Unix(1770000000, 0)
	seed(t, db, chat, 82, 20)
	reserve := func(owner, msg int64) *PendingReply {
		t.Helper()
		var got *PendingReply
		err := db.Update(func(tx *bolt.Tx) error {
			var err error
			got, err = ReserveReply(tx, chat, owner, msg, t0)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	if got := reserve(82, 200); got != nil {
		t.Fatalf("reserved for member without phrase: %+v", got)
	}
	if _, err := spinOnce(t, db, chat, 82, "off", t0); err != nil {
		t.Fatal(err)
	}
	if got := reserve(82, 201); got != nil {
		t.Fatalf("reserved while disabled: %+v", got)
	}
	if _, err := apply(t, db, gameengine.Action{ChatID: chat, UserID: 82, Key: "e", Verb: "equip", Arg: "1", Now: t0}); err != nil {
		t.Fatal(err)
	}
	if _, err := apply(t, db, gameengine.Action{ChatID: chat, UserID: 82, Key: "on", Verb: "enable", Now: t0}); err != nil {
		t.Fatal(err)
	}
	if got := reserve(82, 202); got == nil {
		t.Fatal("no reservation after enable")
	}
}

func TestDeliveryPhraseRechecksLiveState(t *testing.T) {
	deterministicPick(t)
	db := testDB(t)
	const chat, user = int64(91), int64(92)
	t0 := time.Unix(1770000000, 0)
	arm(t, db, chat, user, t0)

	var reserved *PendingReply
	if err := db.Update(func(tx *bolt.Tx) error {
		var err error
		reserved, err = ReserveReply(tx, chat, user, 300, t0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if reserved == nil {
		t.Fatal("no reservation")
	}

	delivery := func() (string, bool) {
		t.Helper()
		var phrase string
		var ok bool
		if err := db.View(func(tx *bolt.Tx) error {
			var err error
			phrase, ok, err = DeliveryPhrase(tx, chat, user)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return phrase, ok
	}

	phrase, ok := delivery()
	if !ok || phrase != reserved.Phrase {
		t.Fatalf("delivery before disable = %q %v", phrase, ok)
	}
	if _, err := apply(t, db, gameengine.Action{ChatID: chat, UserID: user, Key: "off", Verb: "disable", Now: t0.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if phrase, ok := delivery(); ok || phrase != "" {
		t.Fatalf("queued reply delivered after disable: %q %v", phrase, ok)
	}
	if _, err := apply(t, db, gameengine.Action{ChatID: chat, UserID: user, Key: "on", Verb: "enable", Now: t0.Add(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if phrase, ok := delivery(); !ok || phrase != reserved.Phrase {
		t.Fatalf("delivery after re-enable = %q %v", phrase, ok)
	}
}

func TestRejectionIsTyped(t *testing.T) {
	db := testDB(t)
	_, err := apply(t, db, gameengine.Action{ChatID: 0, UserID: 1, Key: "bad", Verb: "view", Now: time.Unix(1770000000, 0)})
	var rej gameengine.Rejection
	if !errors.As(err, &rej) {
		t.Fatalf("err = %v, want Rejection", err)
	}
}
