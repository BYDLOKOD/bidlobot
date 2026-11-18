// Package automat implements the "Автомат отмазок" game: a paid spin over a
// finite no-duplicate excuse pool, a per-chat collection, phrase equipping and
// an opt-in automatic reply when the equipped member is tagged.
package automat

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/gameengine"
)

// ModuleName is the engine registration key and the state bucket name.
const ModuleName = "automat"

const (
	// SpinPrice is the wallet cost of one confirmed spin.
	SpinPrice = 8
	// ChatCooldown bounds automatic replies per chat.
	ChatCooldown = 30 * time.Second
	// TargetCooldown bounds automatic replies per tagged member.
	TargetCooldown = 10 * time.Minute
)

const (
	playersBucket = "players"
	dedupBucket   = "dedup"
	chatCoolBkt   = "chatcool"
	targetCoolBkt = "targetcool"
	chatCoolKey   = "last"
)

// player is the per-chat, per-user automat state.
type player struct {
	enabled  bool
	equipped int    // item ID, 0 = nothing equipped
	mask     uint32 // bit (ID-1) is set when the item is collected
}

func fullMask() uint32 {
	return 1<<uint(len(catalogue)) - 1
}

func collectedCount(mask uint32) int {
	n := 0
	for mask != 0 {
		mask &= mask - 1
		n++
	}
	return n
}

// pickIndex chooses a uniform index in [0,n). Tests override it to make the
// awarded item deterministic.
var pickIndex = securePickIndex

func securePickIndex(n int) (int, error) {
	if n <= 0 {
		return 0, fmt.Errorf("automat: empty pick range")
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0, err
	}
	return int(v.Int64()), nil
}

func pickUnowned(mask uint32) (Item, error) {
	remaining := len(catalogue) - collectedCount(mask)
	if remaining <= 0 {
		return Item{}, gameengine.Reject("Коллекция собрана. Репутация не списана.")
	}
	idx, err := pickIndex(remaining)
	if err != nil {
		return Item{}, err
	}
	for _, it := range catalogue {
		if mask&(1<<uint(it.ID-1)) != 0 {
			continue
		}
		if idx == 0 {
			return it, nil
		}
		idx--
	}
	return Item{}, fmt.Errorf("automat: pool index out of range")
}

// Apply runs one validated automat action inside the caller's write
// transaction. Wallet debits go through the engine, never directly.
func Apply(tx *bolt.Tx, a gameengine.Action) (gameengine.Result, error) {
	if a.ChatID <= 0 || a.UserID <= 0 {
		return gameengine.Result{}, gameengine.Reject(`Некорректный чат или участник.`)
	}
	st, err := gameengine.State(tx, a.ChatID, ModuleName)
	if err != nil {
		return gameengine.Result{}, err
	}
	if st == nil {
		return gameengine.Result{}, fmt.Errorf("automat: state bucket unavailable")
	}
	switch a.Verb {
	case "view", "collection":
		p, err := loadPlayer(st, a.UserID)
		if err != nil {
			return gameengine.Result{}, err
		}
		if a.Verb == "collection" {
			return renderCollection(p), nil
		}
		return renderView(p), nil
	case "spin":
		return spinPrompt(tx, st, a)
	case "confirm":
		return confirmSpin(tx, st, a)
	case "equip":
		return equipPhrase(st, a)
	case "enable":
		return setEnabled(st, a, true)
	case "disable":
		return setEnabled(st, a, false)
	default:
		return gameengine.Result{}, gameengine.Reject(`Неизвестное действие автомата.`)
	}
}

func spinPrompt(tx *bolt.Tx, st *bolt.Bucket, a gameengine.Action) (gameengine.Result, error) {
	p, err := loadPlayer(st, a.UserID)
	if err != nil {
		return gameengine.Result{}, err
	}
	if p.mask == fullMask() {
		return gameengine.Result{}, gameengine.Reject("Коллекция собрана. Репутация не списана.")
	}
	w, err := gameengine.Wallet(tx, a.ChatID, a.UserID)
	if err != nil {
		return gameengine.Result{}, err
	}
	if w.Available < SpinPrice {
		return gameengine.Result{}, gameengine.Reject(fmt.Sprintf(
			"Не хватает репутации. Цена: %s. Доступно: %s.", money(SpinPrice), money(w.Available)))
	}
	remaining := len(catalogue) - collectedCount(p.mask)
	text := fmt.Sprintf("Потратить %s на одну отмазку?\nОсталось %d из %d. Без повторов.",
		money(SpinPrice), remaining, len(catalogue))
	return gameengine.Result{
		Text: text,
		Buttons: []gameengine.Button{
			{Label: "Крутить за " + money(SpinPrice), Verb: "confirm", Arg: spinQuote(p.mask)},
			{Label: "Отмена", Verb: "view"},
		},
	}, nil
}

func confirmSpin(tx *bolt.Tx, st *bolt.Bucket, a gameengine.Action) (gameengine.Result, error) {
	if a.Key == "" {
		return gameengine.Result{}, gameengine.Reject("Не удалось подтвердить: нет ключа запроса. Откройте /excuses заново.")
	}
	p, err := loadPlayer(st, a.UserID)
	if err != nil {
		return gameengine.Result{}, err
	}
	mask, price, ok := strings.Cut(a.Arg, ":")
	expected, err := strconv.ParseUint(mask, 10, 32)
	if !ok || price != strconv.Itoa(SpinPrice) || err != nil {
		return gameengine.Result{}, gameengine.Reject(`Кнопка устарела. Откройте /excuses заново.`)
	}
	if uint32(expected) != p.mask {
		return gameengine.Result{}, gameengine.Reject(`Коллекция изменилась с момента открытия кнопки. Откройте /excuses заново.`)
	}
	if p.mask == fullMask() {
		return gameengine.Result{}, gameengine.Reject("Коллекция собрана. Репутация не списана.")
	}
	w, err := gameengine.Wallet(tx, a.ChatID, a.UserID)
	if err != nil {
		return gameengine.Result{}, err
	}
	if w.Available < SpinPrice {
		return gameengine.Result{}, gameengine.Reject(fmt.Sprintf(
			"Не хватает репутации. Цена: %s. Доступно: %s.", money(SpinPrice), money(w.Available)))
	}
	item, err := pickUnowned(p.mask)
	if err != nil {
		return gameengine.Result{}, err
	}
	if err := gameengine.Adjust(tx, a.ChatID, a.UserID, -SpinPrice, 0); err != nil {
		return gameengine.Result{}, err
	}
	p.mask |= 1 << uint(item.ID-1)
	if err := savePlayer(st, a.UserID, p); err != nil {
		return gameengine.Result{}, err
	}
	text := fmt.Sprintf("%s\n\nСписано %s. В коллекции %d из %d.",
		item.Phrase, money(SpinPrice), collectedCount(p.mask), len(catalogue))
	buttons := make([]gameengine.Button, 0, 3)
	if p.mask != fullMask() {
		buttons = append(buttons, gameengine.Button{Label: "Крутить ещё за " + money(SpinPrice), Verb: "spin"})
	}
	buttons = append(buttons,
		gameengine.Button{Label: "Выбрать для автоответа", Verb: "equip", Arg: strconv.Itoa(item.ID)},
		collectionButton(),
	)
	return gameengine.Result{Text: text, Buttons: buttons}, nil
}

func equipPhrase(st *bolt.Bucket, a gameengine.Action) (gameengine.Result, error) {
	id, err := strconv.Atoi(a.Arg)
	if err != nil {
		return gameengine.Result{}, gameengine.Reject(`Некорректная отмазка.`)
	}
	item, ok := itemByID(id)
	if !ok {
		return gameengine.Result{}, gameengine.Reject(`Такой отмазки нет в коллекции.`)
	}
	p, err := loadPlayer(st, a.UserID)
	if err != nil {
		return gameengine.Result{}, err
	}
	if p.mask&(1<<uint(item.ID-1)) == 0 {
		return gameengine.Result{}, gameengine.Reject(`Эта отмазка ещё не выпадала.`)
	}
	p.equipped = item.ID
	if err := savePlayer(st, a.UserID, p); err != nil {
		return gameengine.Result{}, err
	}
	return renderView(p), nil
}

func setEnabled(st *bolt.Bucket, a gameengine.Action, on bool) (gameengine.Result, error) {
	p, err := loadPlayer(st, a.UserID)
	if err != nil {
		return gameengine.Result{}, err
	}
	if on && p.equipped == 0 {
		return gameengine.Result{}, gameengine.Reject("Сначала выберите фразу из коллекции.")
	}
	p.enabled = on
	if err := savePlayer(st, a.UserID, p); err != nil {
		return gameengine.Result{}, err
	}
	return renderView(p), nil
}

func renderView(p player) gameengine.Result {
	var b strings.Builder
	b.WriteString("Автомат отмазок\n\n")
	buttons := make([]gameengine.Button, 0, 3)
	if remaining := len(catalogue) - collectedCount(p.mask); remaining == 0 {
		b.WriteString("Коллекция собрана. Новых отмазок нет.\n")
	} else {
		fmt.Fprintf(&b, "Одна отмазка за %s. Без повторов.\nВ автомате осталось %d из %d.\n", money(SpinPrice), remaining, len(catalogue))
		buttons = append(buttons, gameengine.Button{Label: "Крутить за " + money(SpinPrice), Verb: "spin"})
	}
	buttons = append(buttons, collectionButton())
	if it, ok := itemByID(p.equipped); ok {
		fmt.Fprintf(&b, "\nДля автоответа выбрана фраза:\n%s\n", it.Phrase)
	}
	if p.enabled {
		b.WriteString("Автоответ: включён. Бот может ответить, когда вас упомянут.")
		buttons = append(buttons, gameengine.Button{Label: "Выключить автоответ", Verb: "disable"})
	} else {
		b.WriteString("Автоответ: выключен.")
		if p.equipped != 0 {
			buttons = append(buttons, gameengine.Button{Label: "Включить автоответ", Verb: "enable"})
		}
	}
	return gameengine.Result{Text: b.String(), Buttons: buttons}
}

func collectionButton() gameengine.Button {
	return gameengine.Button{Label: "Коллекция", Verb: "collection"}
}

func renderCollection(p player) gameengine.Result {
	count := collectedCount(p.mask)
	var b strings.Builder
	fmt.Fprintf(&b, "Коллекция отмазок: %d из %d\n\n", count, len(catalogue))
	buttons := make([]gameengine.Button, 0, count+1)
	if count == 0 {
		b.WriteString("Пока нет отмазок. Первую можно получить в автомате.")
	} else {
		b.WriteString("Выберите фразу для автоответа.\n")
		for _, it := range catalogue {
			if p.mask&(1<<uint(it.ID-1)) == 0 {
				continue
			}
			fmt.Fprintf(&b, "\n%d. %s", it.ID, it.Phrase)
			if it.ID == p.equipped {
				b.WriteString(" (выбрана)")
				continue
			}
			buttons = append(buttons, gameengine.Button{
				Label: fmt.Sprintf("Выбрать №%d", it.ID),
				Verb:  "equip",
				Arg:   strconv.Itoa(it.ID),
			})
		}
	}
	buttons = append(buttons, gameengine.Button{Label: "К автомату", Verb: "view"})
	return gameengine.Result{Text: b.String(), Buttons: buttons}
}

func spinQuote(mask uint32) string {
	return fmt.Sprintf("%d:%d", mask, SpinPrice)
}

func money(n int) string {
	if n < 0 {
		n = 0
	}
	return fmt.Sprintf("%d %s", n, plural(n, "репутация", "репутации", "репутаций"))
}

func plural(n int, one, few, many string) string {
	if n < 0 {
		n = -n
	}
	if n%100 >= 11 && n%100 <= 14 {
		return many
	}
	switch n % 10 {
	case 1:
		return one
	case 2, 3, 4:
		return few
	default:
		return many
	}
}

func userKey(id int64) []byte {
	var k [8]byte
	binary.BigEndian.PutUint64(k[:], uint64(id))
	return k[:]
}

func loadPlayer(st *bolt.Bucket, userID int64) (player, error) {
	pb := st.Bucket([]byte(playersBucket))
	if pb == nil {
		return player{}, nil
	}
	raw := pb.Get(userKey(userID))
	if len(raw) != 6 {
		return player{}, nil
	}
	return player{
		enabled:  raw[0]&1 != 0,
		equipped: int(raw[1]),
		mask:     binary.BigEndian.Uint32(raw[2:6]),
	}, nil
}

func savePlayer(st *bolt.Bucket, userID int64, p player) error {
	pb, err := st.CreateBucketIfNotExists([]byte(playersBucket))
	if err != nil {
		return err
	}
	var raw [6]byte
	if p.enabled {
		raw[0] = 1
	}
	raw[1] = byte(p.equipped)
	binary.BigEndian.PutUint32(raw[2:6], p.mask)
	return pb.Put(userKey(userID), raw[:])
}
