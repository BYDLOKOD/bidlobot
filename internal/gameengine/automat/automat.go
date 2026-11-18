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
		return Item{}, gameengine.Reject(`Коллекция собрана: крутить нечего, репутация не списана.`)
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
	case "view":
		p, err := loadPlayer(st, a.UserID)
		if err != nil {
			return gameengine.Result{}, err
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
		return gameengine.Result{}, gameengine.Reject(`Коллекция собрана: все 12 отмазок уже есть. Крутка не нужна, репутация не списана.`)
	}
	w, err := gameengine.Wallet(tx, a.ChatID, a.UserID)
	if err != nil {
		return gameengine.Result{}, err
	}
	if w.Available < SpinPrice {
		return gameengine.Result{}, gameengine.Reject(fmt.Sprintf(
			`Не хватает репутации: крутка стоит %s, на счету %s. Получите репутацию через /praise.`,
			money(SpinPrice), money(w.Available)))
	}
	remaining := len(catalogue) - collectedCount(p.mask)
	text := fmt.Sprintf("Крутка стоит %s. В пуле осталось %d %s.\nВыпавшая отмазка сразу попадёт в коллекцию.",
		money(SpinPrice), remaining, plural(remaining, "отмазка", "отмазки", "отмазок"))
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
		return gameengine.Result{}, gameengine.Reject(`Не удалось подтвердить крутку: нет ключа запроса. Откройте /excuses заново.`)
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
		return gameengine.Result{}, gameengine.Reject(`Коллекция собрана: все 12 отмазок уже есть. Репутация не списана.`)
	}
	w, err := gameengine.Wallet(tx, a.ChatID, a.UserID)
	if err != nil {
		return gameengine.Result{}, err
	}
	if w.Available < SpinPrice {
		return gameengine.Result{}, gameengine.Reject(fmt.Sprintf(
			`Не хватает репутации: крутка стоит %s, на счету %s.`,
			money(SpinPrice), money(w.Available)))
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
	count := collectedCount(p.mask)
	text := fmt.Sprintf("Выпала отмазка №%d.\n\u00ab%s\u00bb\nСписано %s. В коллекции %d из %d.",
		item.ID, item.Phrase, money(SpinPrice), count, len(catalogue))
	buttons := []gameengine.Button{
		{Label: "Экипировать", Verb: "equip", Arg: strconv.Itoa(item.ID)},
		{Label: "К коллекции", Verb: "view"},
	}
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
	if p.enabled {
		return renderView(p), nil
	}
	res := renderView(p)
	res.Text = fmt.Sprintf("Экипирована отмазка №%d.\n%s\nЧтобы бот отвечал, включите автоответ.", item.ID, res.Text)
	return res, nil
}

func setEnabled(st *bolt.Bucket, a gameengine.Action, on bool) (gameengine.Result, error) {
	p, err := loadPlayer(st, a.UserID)
	if err != nil {
		return gameengine.Result{}, err
	}
	if on && p.equipped == 0 {
		return gameengine.Result{}, gameengine.Reject(`Сначала экипируйте отмазку из коллекции.`)
	}
	p.enabled = on
	if err := savePlayer(st, a.UserID, p); err != nil {
		return gameengine.Result{}, err
	}
	return renderView(p), nil
}

func renderView(p player) gameengine.Result {
	count := collectedCount(p.mask)
	var b []byte
	b = append(b, "Автомат отмазок\n"...)
	b = append(b, fmt.Sprintf("Коллекция: %d из %d.\n", count, len(catalogue))...)
	for _, it := range catalogue {
		if p.mask&(1<<uint(it.ID-1)) == 0 {
			continue
		}
		b = append(b, fmt.Sprintf("%d. %s\n", it.ID, it.Phrase)...)
	}
	if it, ok := itemByID(p.equipped); ok {
		b = append(b, fmt.Sprintf("Экипировано: №%d \u00ab%s\u00bb\n", it.ID, it.Phrase)...)
	} else {
		b = append(b, "Экипировано: пока ничего.\n"...)
	}
	if p.enabled {
		b = append(b, "Автоответ: включён.\n"...)
	} else {
		b = append(b, "Автоответ: выключен.\n"...)
	}
	if p.mask == fullMask() {
		b = append(b, "Пул собран целиком, новых круток нет."...)
	} else {
		b = append(b, fmt.Sprintf("Крутка: %s.\n", money(SpinPrice))...)
	}

	var buttons []gameengine.Button
	if p.mask != fullMask() {
		buttons = append(buttons, gameengine.Button{Label: "Крутить за " + money(SpinPrice), Verb: "spin"})
	}
	for _, it := range catalogue {
		if p.mask&(1<<uint(it.ID-1)) == 0 {
			continue
		}
		buttons = append(buttons, gameengine.Button{
			Label: fmt.Sprintf("Экипировать №%d", it.ID),
			Verb:  "equip",
			Arg:   strconv.Itoa(it.ID),
		})
	}
	if p.enabled {
		buttons = append(buttons, gameengine.Button{Label: "Выключить автоответ", Verb: "disable"})
	} else if p.equipped != 0 {
		buttons = append(buttons, gameengine.Button{Label: "Включить автоответ", Verb: "enable"})
	}
	return gameengine.Result{Text: string(b), Buttons: buttons}
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
