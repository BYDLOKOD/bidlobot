package farmweb

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/gameengine"
	"github.com/veschin/bidlobot/internal/gameengine/farm"
)

type Server struct {
	engine  *gameengine.Engine
	base    string
	secret  []byte
	launch  cipher.AEAD
	member  func(context.Context, int64, int64) bool
	board   func(*bolt.Tx, int64, int64, time.Time) (any, error)
	parse   func(string) (string, string, error)
	display func(context.Context, int64, int64) string
}

func New(engine *gameengine.Engine, botName, appName, token string, member func(context.Context, int64, int64) bool, board func(*bolt.Tx, int64, int64, time.Time) (any, error), parse func(string) (string, string, error)) (*Server, error) {
	valid := func(s string) bool {
		if s == "" {
			return false
		}
		for _, r := range s {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
				return false
			}
		}
		return true
	}
	if !valid(botName) || !valid(appName) || token == "" {
		return nil, errors.New("invalid farm Mini App registration")
	}
	k := mac([]byte(token), "bidlobot:farm:launch:v1")
	c, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCMWithRandomNonce(c)
	if err != nil {
		return nil, err
	}
	return &Server{engine: engine, base: "https://t.me/" + botName + "/" + appName, secret: mac([]byte("WebAppData"), token), launch: aead, member: member, board: board, parse: parse}, nil
}

// SetDisplayResolver configures optional yard labels before serving requests.
func (s *Server) SetDisplayResolver(resolve func(context.Context, int64, int64) string) {
	s.display = resolve
}

func mac(key []byte, text string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = io.WriteString(h, text)
	return h.Sum(nil)
}

func (s *Server) Link(chatID int64) string {
	if chatID >= 0 {
		return ""
	}
	clear := binary.BigEndian.AppendUint64(nil, uint64(chatID))
	clear = binary.BigEndian.AppendUint64(clear, uint64(time.Now().Add(24*time.Hour).Unix()))
	return s.base + "?startapp=" + base64.RawURLEncoding.EncodeToString(s.launch.Seal(nil, nil, clear, nil))
}

func (s *Server) authenticate(init string, now time.Time) (int64, int64, error) {
	bad := errors.New("invalid Telegram launch")
	values, err := url.ParseQuery(init)
	if err != nil {
		return 0, 0, bad
	}
	var keys []string
	for k, v := range values {
		if len(v) != 1 {
			return 0, 0, bad
		}
		if k != "hash" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var check strings.Builder
	for i, k := range keys {
		if i > 0 {
			check.WriteByte('\n')
		}
		check.WriteString(k)
		check.WriteByte('=')
		check.WriteString(values.Get(k))
	}
	hash, err := hex.DecodeString(values.Get("hash"))
	if err != nil || !hmac.Equal(hash, mac(s.secret, check.String())) {
		return 0, 0, bad
	}
	auth, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	if err != nil || time.Unix(auth, 0).Before(now.Add(-time.Hour)) || time.Unix(auth, 0).After(now.Add(time.Minute)) {
		return 0, 0, bad
	}
	var user struct {
		ID    int64 `json:"id"`
		IsBot bool  `json:"is_bot"`
	}
	if json.Unmarshal([]byte(values.Get("user")), &user) != nil || user.ID <= 0 || user.IsBot {
		return 0, 0, bad
	}
	data, err := base64.RawURLEncoding.DecodeString(values.Get("start_param"))
	if err != nil {
		return 0, 0, bad
	}
	plain, err := s.launch.Open(nil, nil, data, nil)
	if err != nil || len(plain) != 16 {
		return 0, 0, bad
	}
	chat := int64(binary.BigEndian.Uint64(plain))
	if chat >= 0 || now.Unix() >= int64(binary.BigEndian.Uint64(plain[8:])) {
		return 0, 0, bad
	}
	return chat, user.ID, nil
}

type request struct {
	Init    string `json:"init"`
	ID      string `json:"id"`
	Command string `json:"command"`
	Verb    string `json:"verb"`
	Arg     string `json:"arg"`
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method == http.MethodGet {
		serveAsset(w, r)
		return
	}
	if r.Method != http.MethodPost || (r.URL.Path != "/board" && r.URL.Path != "/action") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	send := func(code int, v any) { w.WriteHeader(code); _ = json.NewEncoder(w).Encode(v) }
	failure := func(code int, text string) { send(code, map[string]string{"error": text}) }
	var input request
	body := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	body.DisallowUnknownFields()
	if err := body.Decode(&input); err != nil || body.Decode(&struct{}{}) != io.EOF {
		failure(400, "Не удалось прочитать действие.")
		return
	}
	now := time.Now().UTC()
	chat, user, err := s.authenticate(input.Init, now)
	if err != nil {
		failure(401, "Откройте ферму заново по кнопке в чате.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if !s.member(ctx, chat, user) {
		failure(403, "Ферма доступна только участникам этого чата.")
		return
	}
	if r.URL.Path == "/board" {
		var board any
		if err := s.engine.View(func(tx *bolt.Tx) error { var err error; board, err = s.board(tx, -chat, user, now); return err }); err != nil {
			failure(500, "Ферма не открылась. Попробуйте ещё раз.")
			return
		}
		if view, ok := board.(*farm.View); ok && s.display != nil {
			for i := range view.Yards {
				yard := &view.Yards[i]
				if yard.OwnerID != user {
					yard.OwnerName = strings.Join(strings.Fields(s.display(ctx, -chat, yard.OwnerID)), " ")
				}
			}
		}
		send(200, board)
		return
	}
	id, err := base64.RawURLEncoding.Strict().DecodeString(input.ID)
	if err != nil || len(id) != 16 || len(input.Command) > 200 || len(input.Verb) > 20 || len(input.Arg) > 120 || (input.Command != "" && input.Verb != "") {
		failure(400, "Откройте действие заново.")
		return
	}
	if input.Command == "" {
		if input.Verb == "" || strings.ContainsAny(input.Verb, " \t\r\n") {
			failure(400, "Откройте действие заново.")
			return
		}
		input.Command = input.Verb + " " + input.Arg
	}
	verb, arg, err := s.parse(input.Command)
	if err != nil {
		failure(400, err.Error())
		return
	}
	result, err := s.engine.Apply("farm", gameengine.Action{ChatID: -chat, UserID: user, Key: "web:" + input.ID, Verb: verb, Arg: arg, Now: now})
	if err != nil {
		var reject gameengine.Rejection
		if errors.As(err, &reject) {
			failure(409, reject.Error())
			return
		}
		failure(500, "Ферма не подтвердила действие. Повторите запрос с теми же данными.")
		return
	}
	send(200, result)
}
