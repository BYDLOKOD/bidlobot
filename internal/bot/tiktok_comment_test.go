package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/testutil"
)

// --- tiktokCommentDecision ------------------------------------------------

func TestTikTokCommentDecision(t *testing.T) {
	tests := []struct {
		name      string
		msg       *telego.Message
		want      bool
		commentID string
	}{
		{
			name:      "comment permalink",
			msg:       ttTestMessage("https://www.tiktok.com/@user/video/123456789?comment_id=9876543210&is_copy_url=1&is_from_webapp=v1"),
			want:      true,
			commentID: "9876543210",
		},
		{
			name:      "app share param",
			msg:       ttTestMessage("https://www.tiktok.com/@user/video/123456789?share_comment_id=777&share_app_id=1233"),
			want:      true,
			commentID: "777",
		},
		{
			name:      "comment id first param",
			msg:       ttTestMessage("https://www.tiktok.com/@user/video/123456789?comment_id=555&foo=bar"),
			want:      true,
			commentID: "555",
		},
		{
			name: "plain video url falls through to replayer",
			msg:  ttTestMessage("https://www.tiktok.com/@user/video/123456789"),
			want: false,
		},
		{
			name: "short link with comment id ignored",
			msg:  ttTestMessage("https://vm.tiktok.com/ABCDEF/?comment_id=1"),
			want: false,
		},
		{
			name: "empty comment id ignored",
			msg:  ttTestMessage("https://www.tiktok.com/@user/video/123456789?comment_id="),
			want: false,
		},
		{
			name: "non-tiktok host with comment id",
			msg:  ttTestMessage("https://tiktok.com.ru/fake?comment_id=9"),
			want: false,
		},
		{
			name: "bot sender",
			msg: &telego.Message{
				MessageID: 1,
				Chat:      telego.Chat{ID: -100123, Type: telego.ChatTypeSupergroup},
				From:      &telego.User{ID: 100, IsBot: true},
				Text:      "https://www.tiktok.com/@user/video/123?comment_id=9",
			},
			want: false,
		},
		{
			name: "nil message",
			msg:  nil,
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			act, _, cid := tiktokCommentDecision(tc.msg)
			if act != tc.want {
				t.Fatalf("act = %v, want %v", act, tc.want)
			}
			if act && cid != tc.commentID {
				t.Fatalf("commentID = %q, want %q", cid, tc.commentID)
			}
		})
	}
}

func TestTikTokCommentDecisionCaptionEntity(t *testing.T) {
	msg := ttTestMessage("")
	msg.Caption = "look at this"
	msg.CaptionEntities = []telego.MessageEntity{
		{Type: "url", Offset: 0, Length: 5, URL: "https://www.tiktok.com/@u/video/777?comment_id=42&is_copy_url=1"},
	}
	act, videoURL, cid := tiktokCommentDecision(msg)
	if !act || cid != "42" {
		t.Fatalf("act=%v cid=%q", act, cid)
	}
	// The video URL is canonicalized: tracking query stripped (tikwm form).
	if videoURL != "https://www.tiktok.com/@u/video/777" {
		t.Fatalf("url = %q", videoURL)
	}
}

// --- caption --------------------------------------------------------------

func TestTikTokCommentCaption(t *testing.T) {
	if got := tiktokCommentCaption("bayan", "привет <b>жиза</b>"); got != "👤 <b>@bayan</b> писал:\nпривет &lt;b&gt;жиза&lt;/b&gt;" {
		t.Fatalf("caption = %q", got)
	}
	if got := tiktokCommentCaption("bayan", ""); got != "👤 <b>@bayan</b> писал:" {
		t.Fatalf("image-only caption = %q", got)
	}
}

// --- fetch ----------------------------------------------------------------

// tikwmStub serves scripted comment pages. listPages maps top-level
// cursor -> body; replyPages maps "<parentID>:<cursor>" -> body. Every
// request path+query is recorded for assertions.
type tikwmStub struct {
	mu         sync.Mutex
	requests   []string
	listPages  map[string]string
	replyPages map[string]string
}

func (s *tikwmStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.mu.Lock()
	s.requests = append(s.requests, r.URL.Path+"?"+r.URL.RawQuery)
	var body string
	var ok bool
	switch r.URL.Path {
	case tikwmPathList:
		body, ok = s.listPages[q.Get("cursor")]
	case tikwmPathReply:
		body, ok = s.replyPages[q.Get("comment_id")+":"+q.Get("cursor")]
	}
	s.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, body)
}

func (s *tikwmStub) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func tikwmPage(comments []tikwmComment, cursor int, hasMore bool) string {
	var page tikwmCommentPage
	page.Code = 0
	page.Data.Comments = comments
	page.Data.Cursor = cursor
	page.Data.HasMore = hasMore
	b, _ := json.Marshal(page)
	return string(b)
}

func ttComment(id, handle, text string, images ...string) tikwmComment {
	return tikwmComment{ID: id, Text: text, User: tikwmCommentUser{UniqueID: handle}, Images: images}
}

func withTikwmStub(t *testing.T, listPages, replyPages map[string]string) (*tikwmStub, *httptest.Server) {
	t.Helper()
	if replyPages == nil {
		replyPages = map[string]string{}
	}
	stub := &tikwmStub{listPages: listPages, replyPages: replyPages}
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	prevHost, prevInterval := tikwmAPIHost, tikwmMinInterval
	tikwmAPIHost, tikwmMinInterval = srv.URL, 0
	t.Cleanup(func() { tikwmAPIHost, tikwmMinInterval = prevHost, prevInterval })
	return stub, srv
}

func TestFetchTikTokCommentPaginatesByCursor(t *testing.T) {
	stub, _ := withTikwmStub(t, map[string]string{
		"0": tikwmPage([]tikwmComment{ttComment("a", "u1", "one")}, 2, true),
		"2": tikwmPage([]tikwmComment{ttComment("b", "u2", "two"), ttComment("target", "u3", "three", "https://cdn/x.jpeg")}, 4, false),
	}, nil)
	got, err := fetchTikTokComment(context.Background(), srv0(), "https://www.tiktok.com/@u/video/1", "target")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got.ID != "target" || got.User.UniqueID != "u3" || got.Text != "three" || len(got.Images) != 1 {
		t.Fatalf("comment = %+v", got)
	}
	if stub.requestCount() != 2 {
		t.Fatalf("requests = %d, want 2", stub.requestCount())
	}
	if !strings.Contains(stub.requests[1], "cursor=2") {
		t.Fatalf("second page = %q, want cursor=2 (returned cursor drives pagination)", stub.requests[1])
	}
	if !strings.Contains(stub.requests[0], "url=https%3A%2F%2Fwww.tiktok.com%2F%40u%2Fvideo%2F1") {
		t.Fatalf("list request = %q", stub.requests[0])
	}
}

func TestFetchTikTokCommentNotFound(t *testing.T) {
	_, _ = withTikwmStub(t, map[string]string{
		"0": tikwmPage([]tikwmComment{ttComment("a", "u1", "one")}, 1, false),
	}, nil)
	if _, err := fetchTikTokComment(context.Background(), srv0(), "https://www.tiktok.com/@u/video/1", "nope"); err != errTikTokCommentNotFound {
		t.Fatalf("err = %v, want errTikTokCommentNotFound", err)
	}
}

func TestFetchTikTokCommentAPIError(t *testing.T) {
	_, _ = withTikwmStub(t, map[string]string{
		"0": `{"code":1001,"msg":"url invalid"}`,
	}, nil)
	if _, err := fetchTikTokComment(context.Background(), srv0(), "https://www.tiktok.com/@u/video/1", "x"); err == nil || !strings.Contains(err.Error(), "1001") {
		t.Fatalf("err = %v, want code 1001 error", err)
	}
}

// --- pipeline -------------------------------------------------------------

// recCommentSender records sends and returns configurable message IDs.
type recCommentSender struct {
	mu         sync.Mutex
	Messages   []*telego.SendMessageParams
	Photos     []*telego.SendPhotoParams
	Animations []*telego.SendAnimationParams
	Groups     []*telego.SendMediaGroupParams
	Deletes    []*telego.DeleteMessageParams
	nextID     int
}

func newRecCommentSender() *recCommentSender { return &recCommentSender{nextID: 500} }

func (s *recCommentSender) send() *telego.Message {
	s.nextID++
	return &telego.Message{MessageID: s.nextID}
}

func (s *recCommentSender) SendMessage(_ context.Context, p *telego.SendMessageParams) (*telego.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = append(s.Messages, p)
	return s.send(), nil
}

func (s *recCommentSender) SendPhoto(_ context.Context, p *telego.SendPhotoParams) (*telego.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Photos = append(s.Photos, p)
	return s.send(), nil
}

func (s *recCommentSender) SendAnimation(_ context.Context, p *telego.SendAnimationParams) (*telego.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Animations = append(s.Animations, p)
	return s.send(), nil
}

func (s *recCommentSender) SendVideo(_ context.Context, _ *telego.SendVideoParams) (*telego.Message, error) {
	return s.send(), nil
}

func (s *recCommentSender) SendDocument(_ context.Context, _ *telego.SendDocumentParams) (*telego.Message, error) {
	return s.send(), nil
}

func (s *recCommentSender) SendMediaGroup(_ context.Context, p *telego.SendMediaGroupParams) ([]telego.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Groups = append(s.Groups, p)
	out := make([]telego.Message, 0, len(p.Media))
	for range p.Media {
		out = append(out, *s.send())
	}
	return out, nil
}

func (s *recCommentSender) DeleteMessage(_ context.Context, p *telego.DeleteMessageParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Deletes = append(s.Deletes, p)
	return nil
}

// recOwners records RecordOwner calls.
type recOwners struct {
	mu    sync.Mutex
	calls []string // "chatID:msgID:userID"
}

func (r *recOwners) RecordOwner(chatID int64, msgID int, u *telego.User) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, fmt.Sprintf("%d:%d:%d", chatID, msgID, u.ID))
}

func (r *recOwners) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func ttCommentMsg() *telego.Message {
	msg := ttTestMessage("https://www.tiktok.com/@u/video/777?comment_id=42")
	msg.From = &telego.User{ID: 7, Username: "sender", FirstName: "Sender"}
	return msg
}

func srv0() *http.Client { return &http.Client{} }

func TestProcessTikTokCommentTextOnly(t *testing.T) {
	_, srv := withTikwmStub(t, map[string]string{
		"0": tikwmPage([]tikwmComment{ttComment("42", "commenter", "жиза <вот это> да")}, 1, false),
	}, nil)
	snd := newRecCommentSender()
	owners := &recOwners{}
	msg := ttCommentMsg()

	processTikTokComment(context.Background(), snd, discardLogger(), srv.Client(), owners, msg,
		"https://www.tiktok.com/@u/video/777", "42")

	if len(snd.Messages) != 1 {
		t.Fatalf("messages = %+v", snd.Messages)
	}
	if snd.Messages[0].ParseMode != telego.ModeHTML ||
		snd.Messages[0].Text != "👤 <b>@commenter</b> писал:\nжиза &lt;вот это&gt; да" {
		t.Fatalf("quote = %+v", snd.Messages[0])
	}
	if calls := owners.recorded(); len(calls) != 1 || calls[0] != "-1001234567890:501:7" {
	}
	if len(snd.Deletes) != 1 || snd.Deletes[0].MessageID != 42 {
		t.Fatalf("deletes = %+v", snd.Deletes)
	}
}

func TestProcessTikTokCommentSinglePhoto(t *testing.T) {
	_, srv := withTikwmStub(t, map[string]string{
		"0": tikwmPage([]tikwmComment{ttComment("42", "commenter", "", "https://cdn.example/x.jpeg")}, 1, false),
	}, nil)
	snd := newRecCommentSender()
	msg := ttCommentMsg()

	processTikTokComment(context.Background(), snd, discardLogger(), srv.Client(), nil, msg,
		"https://www.tiktok.com/@u/video/777", "42")

	if len(snd.Photos) != 1 || snd.Photos[0].Photo.URL != "https://cdn.example/x.jpeg" {
		t.Fatalf("photos = %+v", snd.Photos)
	}
	if snd.Photos[0].Caption != "👤 <b>@commenter</b> писал:" {
		t.Fatalf("caption = %q", snd.Photos[0].Caption)
	}
	if len(snd.Deletes) != 1 {
		t.Fatalf("original must be deleted after quote, deletes = %+v", snd.Deletes)
	}
}

func TestProcessTikTokCommentGIF(t *testing.T) {
	_, srv := withTikwmStub(t, map[string]string{
		"0": tikwmPage([]tikwmComment{ttComment("42", "commenter", "lo", "https://cdn.example/x.gif")}, 1, false),
	}, nil)
	snd := newRecCommentSender()
	msg := ttCommentMsg()

	processTikTokComment(context.Background(), snd, discardLogger(), srv.Client(), nil, msg,
		"https://www.tiktok.com/@u/video/777", "42")

	if len(snd.Animations) != 1 || snd.Animations[0].Animation.URL != "https://cdn.example/x.gif" {
		t.Fatalf("animations = %+v", snd.Animations)
	}
	if len(snd.Photos) != 0 {
		t.Fatalf("gif must go as animation, photos = %+v", snd.Photos)
	}
}

func TestProcessTikTokCommentAlbum(t *testing.T) {
	_, srv := withTikwmStub(t, map[string]string{
		"0": tikwmPage([]tikwmComment{ttComment("42", "commenter", "two imgs",
			"https://cdn.example/1.jpeg", "https://cdn.example/2.jpeg")}, 1, false),
	}, nil)
	snd := newRecCommentSender()
	owners := &recOwners{}
	msg := ttCommentMsg()

	processTikTokComment(context.Background(), snd, discardLogger(), srv.Client(), owners, msg,
		"https://www.tiktok.com/@u/video/777", "42")

	if len(snd.Groups) != 1 || len(snd.Groups[0].Media) != 2 {
		t.Fatalf("groups = %+v", snd.Groups)
	}
	first := snd.Groups[0].Media[0].(*telego.InputMediaPhoto)
	if first.Caption != "👤 <b>@commenter</b> писал:\ntwo imgs" {
		t.Fatalf("album caption = %q", first.Caption)
	}
	// Both album messages carry the owner.
	if calls := owners.recorded(); len(calls) != 2 {
		t.Fatalf("owner calls = %v", calls)
	}
}

func TestProcessTikTokCommentNotFoundDeclines(t *testing.T) {
	_, srv := withTikwmStub(t, map[string]string{
		"0": tikwmPage([]tikwmComment{ttComment("a", "u1", "one")}, 1, false),
	}, nil)
	snd := newRecCommentSender()
	owners := &recOwners{}
	msg := ttCommentMsg()

	processTikTokComment(context.Background(), snd, discardLogger(), srv.Client(), owners, msg,
		"https://www.tiktok.com/@u/video/777", "nope")

	if len(snd.Deletes) != 0 {
		t.Fatalf("original must stay on failure, deletes = %+v", snd.Deletes)
	}
	if len(owners.recorded()) != 0 {
		t.Fatalf("no owner on failure, calls = %v", owners.recorded())
	}
	if len(snd.Messages) != 1 {
		t.Fatalf("expected one decline message, got %+v", snd.Messages)
	}
}
func TestTikTokReposterRoutesCommentPermalinkToQuote(t *testing.T) {
	_, _ = withTikwmStub(t, map[string]string{
		"0": tikwmPage([]tikwmComment{ttComment("42", "commenter", "hi")}, 1, false),
	}, nil)
	api := testutil.NewMockAPI()
	a := &App{sender: api, log: discardLogger()}
	msg := ttCommentMsg()
	group := &th.HandlerGroup{}
	group.Use(tiktokReposter(a))
	if err := group.HandleUpdate(context.Background(), nil, telego.Update{
		UpdateID: 1,
		Message:  msg,
	}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && api.CallCount("SendMessage") == 0 {
		time.Sleep(time.Millisecond)
	}
	var got string
	for _, c := range api.Calls {
		if c.Method == "SendMessage" {
			if p, ok := c.Params.(*telego.SendMessageParams); ok {
				got = p.Text
			}
		}
	}
	if got != "👤 <b>@commenter</b> писал:\nhi" {
		t.Fatalf("quote = %q", got)
	}
}

// --- short links and reply threads ------------------------------------------

func TestTikTokCommentIDFromURL(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantOK    bool
		wantVideo string
		wantCID   string
	}{
		{
			name:      "web permalink",
			raw:       "https://www.tiktok.com/@u/video/111?comment_id=22&is_copy_url=1",
			wantOK:    true,
			wantVideo: "https://www.tiktok.com/@u/video/111",
			wantCID:   "22",
		},
		{
			name:      "app share permalink",
			raw:       "https://www.tiktok.com/@u/video/111?share_comment_id=33&share_app_id=1233",
			wantOK:    true,
			wantVideo: "https://www.tiktok.com/@u/video/111",
			wantCID:   "33",
		},
		{name: "plain video", raw: "https://www.tiktok.com/@u/video/111"},
		{name: "short link", raw: "https://vt.tiktok.com/ZSabc/"},
		{name: "non-video path", raw: "https://www.tiktok.com/@u?comment_id=9"},
		{name: "foreign host", raw: "https://tiktok.com.ru/x/video/1?comment_id=9"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			video, cid, ok := tiktokCommentIDFromURL(tc.raw)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && (video != tc.wantVideo || cid != tc.wantCID) {
				t.Fatalf("video=%q cid=%q", video, cid)
			}
		})
	}
}

func TestIsTikTokShortLink(t *testing.T) {
	if !isTikTokShortLink("https://vt.tiktok.com/ZS9B6twKLCemh-cIAnX") {
		t.Fatal("vt link must be short")
	}
	if !isTikTokShortLink("https://vm.tiktok.com/ABCDEF/") {
		t.Fatal("vm link must be short")
	}
	if isTikTokShortLink("https://www.tiktok.com/@u/video/111?comment_id=1") {
		t.Fatal("canonical link is not short")
	}
}

func TestTikTokVideoID(t *testing.T) {
	if got := tiktokVideoID("https://www.tiktok.com/@romasrdk/video/7677856345864834325?x=1"); got != "7677856345864834325" {
		t.Fatalf("id = %q", got)
	}
	if got := tiktokVideoID("https://vt.tiktok.com/abc/"); got != "" {
		t.Fatalf("short link id = %q, want empty", got)
	}
}

func TestResolveTikTokURLFollowsRedirects(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "video page")
	}))
	defer final.Close()
	hop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL+"/@u/video/111?share_comment_id=42", http.StatusFound)
	}))
	defer hop.Close()

	got := resolveTikTokURL(context.Background(), &http.Client{}, hop.URL+"/ZSabc/")
	if got != final.URL+"/@u/video/111?share_comment_id=42" {
		t.Fatalf("resolved = %q", got)
	}
}

func TestFetchTikTokCommentScansReplyThreads(t *testing.T) {
	parent := ttComment("parent1", "author", "top comment")
	parent.ReplyTotal = 2
	stub, _ := withTikwmStub(t,
		map[string]string{
			"0": tikwmPage([]tikwmComment{parent}, 1, false),
		},
		map[string]string{
			"parent1:0": tikwmPage([]tikwmComment{ttComment("r1", "u1", "wrong"), ttComment("reply-target", "u2", "right one")}, 2, false),
		})
	got, err := fetchTikTokComment(context.Background(), srv0(), "https://www.tiktok.com/@u/video/111", "reply-target")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got.ID != "reply-target" || got.Text != "right one" {
		t.Fatalf("comment = %+v", got)
	}
	if stub.requestCount() != 2 {
		t.Fatalf("requests = %d, want 2 (list + one reply page)", stub.requestCount())
	}
	if !strings.Contains(stub.requests[1], "video_id=111") || !strings.Contains(stub.requests[1], "comment_id=parent1") {
		t.Fatalf("reply request = %q", stub.requests[1])
	}
}
