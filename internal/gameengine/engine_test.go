package gameengine

import (
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestRetryAndRestartSettleOnce(t *testing.T) {
	db, err := bolt.Open(filepath.Join(t.TempDir(), "test.db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Unix(1770000000, 0)
	modules := map[string]Module{"test": func(tx *bolt.Tx, a Action) (Result, error) {
		if a.Verb == "begin" {
			if err := Adjust(tx, a.ChatID, a.UserID, -3, 3); err != nil {
				return Result{}, err
			}
			return Result{Text: "reserved"}, Schedule(tx, "test", Action{
				ChatID: a.ChatID, Key: "timer", Verb: "finish", Now: now.Add(time.Second),
			})
		}
		return Result{Text: "paid"}, Adjust(tx, a.ChatID, 4, 0, -3)
	}}
	a := Action{ChatID: 1, UserID: 4, Key: "purchase", Verb: "begin", Now: now}
	engine := New(db, modules)
	if _, err := engine.Apply("test", a); err != nil {
		t.Fatal(err)
	}
	engine = New(db, modules)
	if got, err := engine.Apply("test", a); err != nil || got.Text != "reserved" {
		t.Fatalf("retry: %+v %v", got, err)
	}
	if _, err := engine.Apply("test", Action{ChatID: 1, UserID: 4, Key: a.Key, Verb: "finish", Now: now}); err == nil {
		t.Fatal("reused key accepted a different action")
	}
	if got, err := engine.Advance(now); err != nil || len(got) != 0 {
		t.Fatalf("early settlement: %v %v", got, err)
	}
	if got, err := engine.Advance(now.Add(time.Second)); err != nil || len(got) != 1 || got[0].Result.Text != "paid" {
		t.Fatalf("deadline: %v %v", got, err)
	}
	if got, err := engine.Advance(now.Add(time.Hour)); err != nil || len(got) != 0 {
		t.Fatalf("repeated deadline: %v %v", got, err)
	}
	if err := db.View(func(tx *bolt.Tx) error {
		account, err := Wallet(tx, 1, 4)
		if account.Available != 7 || account.Reserved != 0 || account.Score != 0 {
			t.Errorf("account after settlement: %+v", account)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRenderAcknowledgementLeavesNewerCardUpdate(t *testing.T) {
	db, err := bolt.Open(filepath.Join(t.TempDir(), "render.db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	modules := map[string]Module{"prod": func(_ *bolt.Tx, a Action) (Result, error) {
		return Result{Text: a.Verb}, nil
	}}
	engine := New(db, modules)
	a := Action{ChatID: 1, UserID: 4, CardID: 200, Key: "open", Verb: "open", Now: time.Unix(1770000000, 0)}
	if _, err := engine.Apply("prod", a); err != nil {
		t.Fatal(err)
	}
	old, err := engine.PendingRenders()
	if err != nil || len(old) != 1 || old[0].CardID != a.CardID {
		t.Fatalf("opening render: %v, %v", old, err)
	}
	a.Key, a.Verb = "join", "join"
	if _, err := engine.Apply("prod", a); err != nil {
		t.Fatal(err)
	}
	engine = New(db, modules)
	if err := engine.AcknowledgeRender(old[0]); err != nil {
		t.Fatal(err)
	}
	pending, err := engine.PendingRenders()
	if err != nil || len(pending) != 1 || pending[0].Result.Text != "join" {
		t.Fatalf("newer render after restart: %v, %v", pending, err)
	}
}
