package farmweb

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/signal"
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
	launch := func(chat, actor int64, at time.Time) string { return signedLaunch(t, server, chat, actor, at) }
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
	for _, fields := range []string{
		`,"id":"AAAAAAAAAAAAAAAAAAAABA","command":"shop"`,
		`,"id":"AAAAAAAAAAAAAAAAAAAACA","verb":"shop"`,
	} {
		if w := call("/action", valid, fields); w.Code != 200 || !strings.Contains(w.Body.String(), "Магазин") {
			t.Fatalf("shop: %d %s", w.Code, w.Body.String())
		}
	}
	if w := call("/board", valid, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"joined":false`) {
		t.Fatalf("shop view created a yard: %d %s", w.Code, w.Body.String())
	}
	if _, err := engine.Apply("farm", gameengine.Action{ChatID: 100123, UserID: 43, Key: "neighbor", Verb: "join", Now: now}); err != nil {
		t.Fatal(err)
	}
	server.SetDisplayResolver(func(_ context.Context, chat, user int64) string {
		if chat != 100123 || user != 43 {
			t.Fatalf("unexpected display lookup: chat=%d user=%d", chat, user)
		}
		return "Ирина"
	})
	if w := call("/board", valid, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"owner_name":"Ирина"`) {
		t.Fatalf("missing neighbor display name: %d %s", w.Code, w.Body.String())
	}
	server.SetDisplayResolver(nil)
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

func signedLaunch(t *testing.T, server *Server, chat, actor int64, at time.Time) string {
	t.Helper()
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

func TestFarmBrowserFixture(t *testing.T) {
	if os.Getenv("FARM_BROWSER_FIXTURE") != "1" {
		t.Skip("set FARM_BROWSER_FIXTURE=1 for an interactive loopback server")
	}
	db, err := bolt.Open(filepath.Join(t.TempDir(), "farm.db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Update(func(tx *bolt.Tx) error {
		rep, err := tx.CreateBucketIfNotExists([]byte("reputation"))
		if err != nil {
			return err
		}
		for _, user := range []int64{42, 43} {
			if err := rep.Put(fmt.Appendf(nil, "r:%020d:%020d", 100123, user), []byte(`{"available":140,"reserved":0,"score":36}`)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	engine := gameengine.New(db, map[string]gameengine.Module{"farm": farm.Apply})
	server, err := New(engine, "farm_test_bot", "farm", "fixture-token", func(_ context.Context, chat, actor int64) bool {
		return chat == -100123 && (actor == 42 || actor == 43)
	}, farm.Board, farm.Parse)
	if err != nil {
		t.Fatal(err)
	}
	server.SetDisplayResolver(func(_ context.Context, _, user int64) string {
		if user == 43 {
			return "Ирина"
		}
		return "Лена"
	})
	now := time.Now()
	for i, step := range []struct {
		user    int64
		ago     time.Duration
		command string
	}{
		{42, 7 * time.Hour, "plot"},
		{42, 7 * time.Hour, "seed"},
		{43, 7 * time.Hour, "plant p1"},
		{42, 5 * time.Hour, "plant p1"},
		{43, 3 * time.Hour, "build mill"},
		{42, 2 * time.Hour, "plant p2"},
		{43, time.Hour, "seed 3"},
		{43, time.Hour, "sell seed 3 2"},
		{43, 5 * time.Minute, "offer mill 1 6 7"},
	} {
		verb, arg, err := farm.Parse(step.command)
		if err == nil {
			_, err = engine.Apply("farm", gameengine.Action{ChatID: 100123, UserID: step.user, Key: fmt.Sprintf("fixture:%d", i), Verb: verb, Arg: arg, Now: now.Add(-step.ago)})
		}
		if err != nil {
			t.Fatalf("%s: %v", step.command, err)
		}
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(t.Context(), os.Interrupt)
	defer stop()
	httpServer := &http.Server{Handler: server, ReadHeaderTimeout: 5 * time.Second}
	defer httpServer.Close()
	go func() { _ = httpServer.Serve(listener) }()
	fragment := url.Values{
		"tgWebAppData":     {signedLaunch(t, server, -100123, 42, time.Now())},
		"tgWebAppVersion":  {"8.0"},
		"tgWebAppPlatform": {"web"},
	}.Encode()
	t.Logf("Open http://%s/#%s", listener.Addr(), fragment)
	<-ctx.Done()
}
