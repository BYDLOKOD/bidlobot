package gameengine

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/domain/reputation"
	"github.com/veschin/bidlobot/internal/storage"
)

// Action is authorized by its transport before Apply. Only the scheduler
// issues actions with no user. Now is server time, not client input.
type Action = storage.GameAction

type Button struct{ Label, Verb, Arg string }

type Result struct {
	Text    string
	Buttons []Button
}

type Module func(*bolt.Tx, Action) (Result, error)

type Engine struct {
	db      *bolt.DB
	modules map[string]Module
}

func New(db *bolt.DB, modules map[string]Module) *Engine {
	return &Engine{db: db, modules: modules}
}

type Rejection string

func (e Rejection) Error() string { return string(e) }

func Reject(text string) error { return Rejection(text) }

var (
	gamesBucket     = []byte(storage.GameStateBucket)
	deadlinesBucket = []byte(storage.GameDeadlineBucket)
	retiredBucket   = []byte(storage.GameRetiredBucket)
)

func number(n int64) []byte { return binary.BigEndian.AppendUint64(nil, uint64(n)) }

func child(tx *bolt.Tx, root, key []byte) (*bolt.Bucket, error) {
	if tx.Writable() {
		b, err := tx.CreateBucketIfNotExists(root)
		if err != nil {
			return nil, err
		}
		return b.CreateBucketIfNotExists(key)
	}
	if b := tx.Bucket(root); b != nil {
		return b.Bucket(key), nil
	}
	return nil, nil
}

func State(tx *bolt.Tx, chatID int64, module string) (*bolt.Bucket, error) {
	if retired := tx.Bucket(retiredBucket); retired != nil && retired.Get(number(chatID)) != nil {
		return nil, Reject("Чат переехал. Откройте игру в новом чате.")
	}
	b, err := child(tx, gamesBucket, number(chatID))
	if err != nil || b == nil {
		return nil, err
	}
	if tx.Writable() {
		return b.CreateBucketIfNotExists([]byte(module))
	}
	return b.Bucket([]byte(module)), nil
}

func Wallet(tx *bolt.Tx, chatID, userID int64) (reputation.Account, error) {
	return storage.ReputationWallet(tx, chatID, userID)
}

func Adjust(tx *bolt.Tx, chatID, userID int64, availableDelta, reservedDelta int) error {
	err := storage.AdjustReputationWallet(tx, chatID, userID, availableDelta, reservedDelta)
	if errors.Is(err, reputation.ErrInsufficientBalance) {
		return Reject("Не хватает репы. Проверьте баланс в /rep.")
	}
	return err
}

type applied struct {
	Verb, Arg string
	Result    Result
}

// Apply commits module state and its reply together. Network delivery never
// enters the transaction, and a retry never spends reputation a second time.
func (e *Engine) Apply(module string, a Action) (Result, error) {
	if a.ChatID <= 0 || a.UserID <= 0 || a.Key == "" || len(a.Key) > 256 || a.Now.Unix() <= 0 {
		return Result{}, Reject("Действие недоступно. Откройте игру заново.")
	}
	var r Result
	if module == "prod" && (a.Verb == "show" || a.Verb == "retro") {
		fn := e.modules[module]
		if fn == nil {
			return r, Reject("Эта игра недоступна.")
		}
		err := e.db.View(func(tx *bolt.Tx) error {
			var err error
			r, err = fn(tx, a)
			return err
		})
		return r, err
	}
	err := e.db.Update(func(tx *bolt.Tx) error {
		var err error
		r, err = e.apply(tx, module, a)
		return err
	})
	return r, err
}

func (e *Engine) View(fn func(*bolt.Tx) error) error { return e.db.View(fn) }

func (e *Engine) Update(fn func(*bolt.Tx) error) error { return e.db.Update(fn) }

func (e *Engine) apply(tx *bolt.Tx, module string, a Action) (Result, error) {
	fn, ok := e.modules[module]
	if !ok {
		return Result{}, Reject("Эта игра недоступна.")
	}
	b, err := State(tx, a.ChatID, "_actions")
	if err != nil {
		return Result{}, err
	}
	key := []byte(fmt.Sprintf("%s:%d:%s", module, a.UserID, a.Key))
	if prev := b.Get(key); prev != nil {
		var record applied
		if err := json.Unmarshal(prev, &record); err != nil {
			return Result{}, err
		}
		if record.Verb != a.Verb || record.Arg != a.Arg {
			return Result{}, Reject("Действие изменилось. Откройте карточку заново.")
		}
		return record.Result, nil
	}
	r, err := fn(tx, a)
	if err != nil {
		return Result{}, err
	}
	if module == "prod" && a.Verb != "show" && a.Verb != "retro" {
		if err := queueRender(tx, a, r); err != nil {
			return Result{}, err
		}
	}
	value, err := json.Marshal(applied{a.Verb, a.Arg, r})
	if err != nil {
		return Result{}, err
	}
	return r, b.Put(key, value)
}

type deadline = storage.GameDeadline

func Schedule(tx *bolt.Tx, module string, a Action) error {
	if a.ChatID <= 0 || a.UserID != 0 || a.Key == "" || a.Now.Unix() <= 0 {
		return errors.New("invalid game deadline")
	}
	b, err := tx.CreateBucketIfNotExists(deadlinesBucket)
	if err != nil {
		return err
	}
	key := append(number(a.Now.UnixNano()), number(a.ChatID)...)
	key = append(key, (module + ":" + a.Key)...)
	v, err := json.Marshal(deadline{Module: module, Action: a})
	if err != nil {
		return err
	}
	if previous := b.Get(key); previous != nil && !bytes.Equal(previous, v) {
		return errors.New("conflicting game deadline")
	}
	return b.Put(key, v)
}

type Settlement struct {
	ChatID int64
	Module string
	Result Result
}

// Advance processes due work in deadline order. Bound each drain so it cannot
// starve chat updates. A failed timer remains queued rather than losing work.
func (e *Engine) Advance(now time.Time) ([]Settlement, error) {
	var completed []Settlement
	for range 32 {
		var item Settlement
		processed := false
		err := e.db.Update(func(tx *bolt.Tx) error {
			b := tx.Bucket(deadlinesBucket)
			if b == nil {
				return nil
			}
			k, v := b.Cursor().First()
			if k == nil || binary.BigEndian.Uint64(k) > uint64(now.UnixNano()) {
				return nil
			}
			key := bytes.Clone(k)
			var d deadline
			if err := json.Unmarshal(v, &d); err != nil {
				return err
			}
			r, err := e.apply(tx, d.Module, d.Action)
			if err != nil {
				return err
			}
			item = Settlement{d.Action.ChatID, d.Module, r}
			processed = true
			return b.Delete(key)
		})
		if err != nil {
			return completed, err
		}
		if !processed {
			break
		}
		if item.Result.Text != "" {
			completed = append(completed, item)
		}
	}
	return completed, nil
}
