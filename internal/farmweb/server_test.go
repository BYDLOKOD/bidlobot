package farmweb

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/gameengine"
	"github.com/veschin/bidlobot/internal/gameengine/farm"
)

func TestFarmWebBindsSignedActorAndChat(t *testing.T) {
	db, err := bolt.Open(filepath.Join(t.TempDir(), "farm.db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	engine := gameengine.New(db, map[string]gameengine.Module{"farm": farm.Apply})
	server, err := New(engine, "farm_test_bot", "farm", "fixture-token", func(_ context.Context, chat, actor int64) bool { return chat == -100123 && actor == 42 }, farm.Board, farm.Parse)
	if err != nil {
		t.Fatal(err)
	}
	launch := func(chat, actor int64, at time.Time) string {
		u, err := url.Parse(server.Link(chat))
		if err != nil {
			t.Fatal(err)
		}
		start := u.Query().Get("startapp")
		user := fmt.Sprintf(`{"id":%d}`, actor)
		stamp := fmt.Sprint(at.Unix())
		check := "auth_date=" + stamp + "\nstart_param=" + start + "\nuser=" + user
		return url.Values{"auth_date": {stamp}, "start_param": {start}, "user": {user}, "hash": {hex.EncodeToString(mac(server.secret, check))}}.Encode()
	}
	call := func(path, init, fields string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(fmt.Sprintf(`{"init":%q%s}`, init, fields)))
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w
	}
	now := time.Now()
	valid := launch(-100123, 42, now)
	if w := call("/board", valid, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"viewer_id":42`) {
		t.Fatalf("board: %d %s", w.Code, w.Body.String())
	}
	if w := call("/board", launch(-100124, 42, now), ""); w.Code != 403 {
		t.Fatalf("cross-chat response: %d", w.Code)
	}
	if w := call("/board", strings.Replace(valid, "%3A42", "%3A43", 1), ""); w.Code != 401 {
		t.Fatalf("changed identity response: %d", w.Code)
	}
	if w := call("/board", launch(-100123, 42, now.Add(-2*time.Hour)), ""); w.Code != 401 {
		t.Fatalf("expired identity response: %d", w.Code)
	}
	const seed = `,"id":"AAAAAAAAAAAAAAAAAAAAAA","command":"seed 2"`
	for range 2 {
		if w := call("/action", valid, seed); w.Code != 200 {
			t.Fatalf("purchase: %d %s", w.Code, w.Body.String())
		}
	}
	if w := call("/board", valid, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"available":2`) {
		t.Fatalf("replayed purchase: %d %s", w.Code, w.Body.String())
	}
	if w := call("/action", valid, `,"id":"AAAAAAAAAAAAAAAAAAAAAQ","command":"settle"`); w.Code != 400 {
		t.Fatalf("internal action response: %d", w.Code)
	}
}
