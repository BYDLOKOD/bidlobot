package bot

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/domain/pending"
)

type fakePendingStore struct {
	mu      sync.Mutex
	data    map[string]*pending.Action
	getErr  error
	deleted []string
}

func newFakePending() *fakePendingStore {
	return &fakePendingStore{data: make(map[string]*pending.Action)}
}

func (s *fakePendingStore) Create(_ context.Context, a pending.Action) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := a
	s.data[a.ID] = &cp
	return nil
}

func (s *fakePendingStore) Get(_ context.Context, id string) (*pending.Action, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.data[id]
	if !ok {
		return nil, pending.ErrNotFound
	}
	if time.Now().UTC().After(a.ExpiresAt) {
		delete(s.data, id)
		return nil, pending.ErrExpired
	}
	cp := *a
	return &cp, nil
}

func (s *fakePendingStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, id)
	s.deleted = append(s.deleted, id)
	return nil
}

func (s *fakePendingStore) GarbageCollect(_ context.Context, _ time.Time) (int, error) {
	return 0, nil
}

func TestParseCallbackData(t *testing.T) {
	cases := []struct {
		in        string
		wantVerb  string
		wantID    string
		wantOK    bool
	}{
		{"v1:apply:abc1234567890def", "apply", "abc1234567890def", true},
		{"v1:cancel:xyz", "cancel", "xyz", true},
		{"v1:preview:id", "preview", "id", true},
		{"v2:apply:id", "", "", false},        // wrong namespace
		{"apply:id", "", "", false},           // missing namespace
		{"v1:apply", "", "", false},           // missing id
		{"v1::id", "", "", false},             // empty verb
		{"v1:apply:", "", "", false},          // empty id
		{"", "", "", false},                   // empty
	}
	for _, c := range cases {
		verb, id, ok := parseCallbackData(c.in)
		if verb != c.wantVerb || id != c.wantID || ok != c.wantOK {
			t.Errorf("parseCallbackData(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.in, verb, id, ok, c.wantVerb, c.wantID, c.wantOK)
		}
	}
}

func TestMakeCallbackRoundTrip(t *testing.T) {
	for _, in := range []struct{ verb, id string }{
		{"apply", "abc1234567890def"},
		{"cancel", "0011223344556677"},
		{"preview", "deadbeefdeadbeef"},
		{"confirm", "ffffffffffffffff"},
	} {
		data := MakeCallback(in.verb, in.id)
		if len(data) > 64 {
			t.Errorf("callback_data exceeds 64 bytes: %d (%q)", len(data), data)
		}
		verb, id, ok := parseCallbackData(data)
		if !ok || verb != in.verb || id != in.id {
			t.Errorf("round-trip fail for %v: got (%q,%q,%v)", in, verb, id, ok)
		}
	}
}

func TestDispatcherUnknownCallbackData(t *testing.T) {
	store := newFakePending()
	d := NewCallbackDispatcher(store, nil, nil, testLogger())

	resp := d.dispatch(context.Background(), telego.CallbackQuery{Data: "garbage"})
	if resp.AnswerText == "" {
		t.Fatal("should answer with toast on garbage callback_data")
	}
}

func TestDispatcherActionExpired(t *testing.T) {
	store := newFakePending()
	store.data["abc"] = &pending.Action{
		ID: "abc", Kind: pending.KindWarn, ActorUserID: 100,
		ExpiresAt: time.Now().Add(-time.Hour),
	}
	d := NewCallbackDispatcher(store, nil, nil, testLogger())

	resp := d.dispatch(context.Background(), telego.CallbackQuery{
		Data: MakeCallback(cbApply, "abc"),
		From: telego.User{ID: 100},
	})
	if !resp.ShowAlert {
		t.Fatal("expired action should show alert")
	}
	if resp.EditedText == "" {
		t.Fatal("expired action should edit message")
	}
}

func TestDispatcherActionNotFound(t *testing.T) {
	store := newFakePending()
	d := NewCallbackDispatcher(store, nil, nil, testLogger())

	resp := d.dispatch(context.Background(), telego.CallbackQuery{
		Data: MakeCallback(cbApply, "missing"),
		From: telego.User{ID: 100},
	})
	if !resp.ShowAlert {
		t.Fatal("missing action should show alert")
	}
}

func TestDispatcherWrongActor(t *testing.T) {
	store := newFakePending()
	store.data["abc"] = &pending.Action{
		ID: "abc", Kind: pending.KindWarn, ActorUserID: 100,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	d := NewCallbackDispatcher(store, nil, nil, testLogger())

	resp := d.dispatch(context.Background(), telego.CallbackQuery{
		Data: MakeCallback(cbApply, "abc"),
		From: telego.User{ID: 200}, // not the actor
	})
	if !resp.ShowAlert {
		t.Fatal("wrong actor should show alert")
	}
}

func TestDispatcherCancelDeletesAction(t *testing.T) {
	store := newFakePending()
	store.data["abc"] = &pending.Action{
		ID: "abc", Kind: pending.KindWarn, ActorUserID: 100,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	d := NewCallbackDispatcher(store, nil, nil, testLogger())

	resp := d.dispatch(context.Background(), telego.CallbackQuery{
		Data: MakeCallback(cbCancel, "abc"),
		From: telego.User{ID: 100},
	})
	if resp.AnswerText == "" || resp.EditedText == "" {
		t.Fatal("cancel should answer + edit message")
	}
	if len(store.deleted) != 1 || store.deleted[0] != "abc" {
		t.Fatalf("cancel must delete action, got deleted=%v", store.deleted)
	}
}

func TestDispatcherUnregisteredExecutor(t *testing.T) {
	store := newFakePending()
	store.data["abc"] = &pending.Action{
		ID: "abc", Kind: pending.KindBan, ActorUserID: 100,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	d := NewCallbackDispatcher(store, nil, nil, testLogger())

	resp := d.dispatch(context.Background(), telego.CallbackQuery{
		Data: MakeCallback(cbApply, "abc"),
		From: telego.User{ID: 100},
	})
	if !resp.ShowAlert {
		t.Fatal("unregistered executor should show alert toast")
	}
}

func TestDispatcherRoutesToCorrectExecutor(t *testing.T) {
	store := newFakePending()
	store.data["abc"] = &pending.Action{
		ID: "abc", Kind: pending.KindWarn, ActorUserID: 100,
		AbsChatID: 1000, ExpiresAt: time.Now().Add(time.Hour),
	}

	cache := stubAdminCache(true)
	d := NewCallbackDispatcher(store, cache, nil, testLogger())

	called := false
	d.Register(pending.KindWarn, cbApply, func(_ context.Context, _ telego.CallbackQuery, a *pending.Action) callbackResponse {
		called = true
		if a.ID != "abc" {
			t.Errorf("executor got wrong action: %+v", a)
		}
		return callbackResponse{AnswerText: "ok"}
	})

	resp := d.dispatch(context.Background(), telego.CallbackQuery{
		Data: MakeCallback(cbApply, "abc"),
		From: telego.User{ID: 100},
	})
	if !called {
		t.Fatal("executor was not called")
	}
	if resp.AnswerText != "ok" {
		t.Fatalf("response from executor not propagated: %+v", resp)
	}
}

func TestDispatcherRejectsCallerNoLongerAdmin(t *testing.T) {
	store := newFakePending()
	store.data["abc"] = &pending.Action{
		ID: "abc", Kind: pending.KindBan, ActorUserID: 100,
		AbsChatID: 1000, ExpiresAt: time.Now().Add(time.Hour),
	}
	cache := stubAdminCache(false) // admin check now denies

	d := NewCallbackDispatcher(store, cache, nil, testLogger())
	d.Register(pending.KindBan, cbApply, func(_ context.Context, _ telego.CallbackQuery, _ *pending.Action) callbackResponse {
		t.Fatal("executor must not be called when admin check denies")
		return callbackResponse{}
	})

	resp := d.dispatch(context.Background(), telego.CallbackQuery{
		Data: MakeCallback(cbApply, "abc"),
		From: telego.User{ID: 100},
	})
	if !resp.ShowAlert {
		t.Fatal("denied admin must show alert")
	}
}

func TestDispatcherStoreErrorReturnsAlert(t *testing.T) {
	store := newFakePending()
	store.getErr = errors.New("disk full")
	d := NewCallbackDispatcher(store, nil, nil, testLogger())

	resp := d.dispatch(context.Background(), telego.CallbackQuery{
		Data: MakeCallback(cbApply, "abc"),
		From: telego.User{ID: 100},
	})
	if !resp.ShowAlert {
		t.Fatal("store error should show alert")
	}
}
