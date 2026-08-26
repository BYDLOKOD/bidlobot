package bot

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"
)

func TestXPostDecision(t *testing.T) {
	regular := func(text string) *telego.Message {
		msg := ytTestMessage(text)
		return msg
	}

	tests := []struct {
		name    string
		msg     *telego.Message
		wantAct bool
		wantURL string
	}{
		{name: "x username status", msg: regular("https://x.com/alice/status/123456"), wantAct: true, wantURL: "https://x.com/alice/status/123456"},
		{name: "twitter over http", msg: regular("http://twitter.com/alice/status/123456"), wantAct: true, wantURL: "http://twitter.com/alice/status/123456"},
		{name: "scheme-less", msg: regular("x.com/alice/status/123456"), wantAct: true, wantURL: "x.com/alice/status/123456"},
		{name: "www prefix", msg: regular("https://www.x.com/alice/status/123456"), wantAct: true, wantURL: "https://www.x.com/alice/status/123456"},
		{name: "mobile prefix", msg: regular("https://mobile.twitter.com/alice/status/123456"), wantAct: true, wantURL: "https://mobile.twitter.com/alice/status/123456"},
		{name: "m prefix and i web status", msg: regular("https://m.x.com/i/web/status/123456"), wantAct: true, wantURL: "https://m.x.com/i/web/status/123456"},
		{name: "caption", msg: func() *telego.Message {
			msg := regular("")
			msg.Caption = "twitter.com/alice/status/123456."
			return msg
		}(), wantAct: true, wantURL: "twitter.com/alice/status/123456"},
		{name: "url entity", msg: func() *telego.Message {
			msg := regular("post")
			msg.Entities = []telego.MessageEntity{{Type: "url", URL: "https://x.com/alice/status/123456"}}
			return msg
		}(), wantAct: true, wantURL: "https://x.com/alice/status/123456"},
		{name: "caption text link entity", msg: func() *telego.Message {
			msg := regular("")
			msg.Caption = "post"
			msg.CaptionEntities = []telego.MessageEntity{{Type: "text_link", URL: "https://twitter.com/i/web/status/123456"}}
			return msg
		}(), wantAct: true, wantURL: "https://twitter.com/i/web/status/123456"},
		{name: "first valid URL", msg: regular("https://x.com/first/status/111 https://twitter.com/second/status/222"), wantAct: true, wantURL: "https://x.com/first/status/111"},
		{name: "nil message", msg: nil},
		{name: "nil sender", msg: &telego.Message{Text: "https://x.com/alice/status/123456"}},
		{name: "bot sender", msg: func() *telego.Message {
			msg := regular("https://x.com/alice/status/123456")
			msg.From.IsBot = true
			return msg
		}()},
		{name: "anonymous admin", msg: func() *telego.Message {
			msg := regular("https://x.com/alice/status/123456")
			msg.From.ID = 1087968824
			return msg
		}()},
		{name: "channel sender", msg: func() *telego.Message {
			msg := regular("https://x.com/alice/status/123456")
			msg.SenderChat = &telego.Chat{ID: -100456}
			return msg
		}()},
		{name: "other scheme", msg: regular("ftp://x.com/alice/status/123456")},
		{name: "lookalike host", msg: regular("https://x.com.example/alice/status/123456")},
		{name: "subdomain suffix", msg: regular("https://foo.x.com/alice/status/123456")},
		{name: "path suffix", msg: regular("https://example.com/x.com/alice/status/123456")},
		{name: "opaque scheme suffix", msg: regular("mailto:x.com/alice/status/123456")},
		{name: "query suffix", msg: regular("https://example.com/?x.com/alice/status/123456")},
		{name: "path parameter suffix", msg: regular("https://example.com/;x.com/alice/status/123456")},
		{name: "missing status", msg: regular("https://x.com/alice/123456")},
		{name: "non-numeric status", msg: regular("https://x.com/alice/status/nope")},
		{name: "missing ID", msg: regular("https://x.com/alice/status/")},
		{name: "non-status path", msg: regular("https://x.com/home")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			act, gotURL := xpostDecision(tt.msg)
			if act != tt.wantAct || gotURL != tt.wantURL {
				t.Fatalf("xpostDecision() = (%v, %q), want (%v, %q)", act, gotURL, tt.wantAct, tt.wantURL)
			}
		})
	}
}

// --- Test infrastructure ---------------------------------------------------

// xpostAPIServer serves one FixTweet API payload for every status path.
func xpostAPIServer(t *testing.T, payload string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	}))
}

// xpostMediaServer stands in for the twimg CDN: TLS (the download path is
// https-only), registered in the media-host allowlist for the test's
// lifetime. It records the requested paths.
func xpostMediaServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var requested []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.URL.Path)
		_, _ = io.WriteString(w, "media bytes")
	}))
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parsing media server URL: %v", err)
	}
	xpostMediaHosts[parsed.Hostname()] = struct{}{}
	t.Cleanup(func() { delete(xpostMediaHosts, parsed.Hostname()) })
	return server, &requested
}

// xpostInsecureClient trusts the media server's self-signed certificate.
func xpostInsecureClient() *http.Client {
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
}

type failingXPostSender struct {
	recYTSender
}

func (f *failingXPostSender) SendMessage(context.Context, *telego.SendMessageParams) (*telego.Message, error) {
	return nil, errors.New("telegram unavailable")
}

type failingPhotoSender struct {
	recYTSender
}

func (f *failingPhotoSender) SendPhoto(_ context.Context, p *telego.SendPhotoParams) (*telego.Message, error) {
	f.Photos = append(f.Photos, p)
	return nil, errors.New("photo send failed")
}

// --- FixTweet fetch ---------------------------------------------------------

func TestFetchXPostTweetFlattensPayload(t *testing.T) {
	payload := `{"code":200,"tweet":{"url":"https://x.com/alice/status/123",` +
		`"text":"Hello world","author":{"name":"Alice","screen_name":"alice"},` +
		`"media":{"photos":[{"url":"https://pbs.twimg.com/media/1.jpg"},{"url":"https://pbs.twimg.com/media/2.jpg"}],` +
		`"videos":[{"url":"https://video.twimg.com/v.mp4","duration":10,"formats":[{"url":"https://video.twimg.com/lo.mp4","bitrate":5,"container":"mp4"}]}]}}}`
	server := xpostAPIServer(t, payload)
	defer server.Close()

	tweet, err := fetchXPostTweet(context.Background(), server.Client(), server.URL, "https://x.com/alice/status/123")
	if err != nil {
		t.Fatalf("fetchXPostTweet: %v", err)
	}
	if tweet.URL != "https://x.com/alice/status/123" || tweet.Text != "Hello world" ||
		tweet.AuthorName != "Alice" || tweet.AuthorHandle != "alice" {
		t.Fatalf("flattened tweet = %+v", tweet)
	}
	if len(tweet.PhotoURLs) != 2 || tweet.PhotoURLs[0] != "https://pbs.twimg.com/media/1.jpg" {
		t.Fatalf("photos = %q", tweet.PhotoURLs)
	}
	if len(tweet.Videos) != 1 || tweet.Videos[0].Formats[0].URL != "https://video.twimg.com/lo.mp4" {
		t.Fatalf("videos = %+v", tweet.Videos)
	}
}

func TestFetchXPostTweetWebStatusPath(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":200,"tweet":{"url":"u","text":"","author":{},"media":{}}}`)
	}))
	defer server.Close()

	if _, err := fetchXPostTweet(context.Background(), server.Client(), server.URL, "https://twitter.com/i/web/status/999"); err != nil {
		t.Fatalf("fetchXPostTweet: %v", err)
	}
	if gotPath != "/i/status/999" {
		t.Fatalf("requested path = %q, want /i/status/999", gotPath)
	}
}

func TestFetchXPostTweetErrors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		payload string
	}{
		{name: "http error", status: http.StatusNotFound, payload: `{"code":404}`},
		{name: "api code not ok", status: http.StatusOK, payload: `{"code":404,"tweet":{"url":"u"}}`},
		{name: "no tweet object", status: http.StatusOK, payload: `{"code":200}`},
		{name: "malformed json", status: http.StatusOK, payload: `not json`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.payload)
			}))
			defer server.Close()
			if _, err := fetchXPostTweet(context.Background(), server.Client(), server.URL, "https://x.com/a/status/1"); err == nil {
				t.Fatalf("expected error for %s", tt.name)
			}
		})
	}
}

// --- Video variant selection -------------------------------------------------

func TestPickXPostVideoVariant(t *testing.T) {
	tests := []struct {
		name    string
		video   xpostVideo
		wantURL string
		wantOK  bool
	}{
		{
			name: "top variant fits",
			video: xpostVideo{Duration: 10, Formats: []xpostFormat{
				{URL: "hi", Bitrate: 1000, Container: "mp4"},
				{URL: "lo", Bitrate: 100, Container: "mp4"},
			}},
			wantURL: "hi", wantOK: true,
		},
		{
			name: "top variant too large, next fits",
			video: xpostVideo{Duration: 30, Formats: []xpostFormat{
				{URL: "hi", Bitrate: 20_000_000, Container: "mp4"},
				{URL: "mid", Bitrate: 2_000_000, Container: "mp4"},
				{URL: "lo", Bitrate: 500_000, Container: "mp4"},
			}},
			wantURL: "mid", wantOK: true,
		},
		{
			name: "every variant too large",
			video: xpostVideo{Duration: 60, Formats: []xpostFormat{
				{URL: "hi", Bitrate: 20_000_000, Container: "mp4"},
			}},
			wantOK: false,
		},
		{
			name:    "no formats falls back to top-level url",
			video:   xpostVideo{URL: "top", Duration: 60},
			wantURL: "top", wantOK: true,
		},
		{
			name: "m3u8 playlists skipped",
			video: xpostVideo{Duration: 10, Formats: []xpostFormat{
				{URL: "pl", Container: "m3u8"},
				{URL: "mp4", Bitrate: 100, Container: "mp4"},
			}},
			wantURL: "mp4", wantOK: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotURL, gotOK := pickXPostVideoVariant(tt.video)
			if gotURL != tt.wantURL || gotOK != tt.wantOK {
				t.Fatalf("pickXPostVideoVariant() = (%q, %v), want (%q, %v)", gotURL, gotOK, tt.wantURL, tt.wantOK)
			}
		})
	}
}

// --- Caption assembly ---------------------------------------------------------

func TestBuildXPostCaption(t *testing.T) {
	const canonical = "https://x.com/alice/status/123456"

	t.Run("basic layout", func(t *testing.T) {
		got := buildXPostCaption("alice", "", "Alice", "alice", "Hello world", canonical, telegoCaptionLimit)
		want := "👤 alice\n\nAlice (@alice)\nHello world\n\n" + canonical
		if got != want {
			t.Fatalf("caption = %q, want %q", got, want)
		}
	})

	t.Run("user text preserved", func(t *testing.T) {
		got := buildXPostCaption("alice", "look at this", "Alice", "alice", "Hello world", canonical, telegoCaptionLimit)
		want := "👤 alice\nlook at this\n\nAlice (@alice)\nHello world\n\n" + canonical
		if got != want {
			t.Fatalf("caption = %q, want %q", got, want)
		}
	})

	t.Run("author without handle", func(t *testing.T) {
		got := buildXPostCaption("alice", "", "Alice", "", "Hello world", canonical, telegoCaptionLimit)
		want := "👤 alice\n\nAlice\nHello world\n\n" + canonical
		if got != want {
			t.Fatalf("caption = %q, want %q", got, want)
		}
	})

	t.Run("long tweet truncated, url and author survive", func(t *testing.T) {
		long := strings.Repeat("word ", 100)
		got := buildXPostCaption("alice", "", "Alice", "alice", long, canonical, 120)
		if utf16Len(got) > 120 {
			t.Fatalf("caption length = %d, want <= 120", utf16Len(got))
		}
		if !strings.HasSuffix(got, canonical) || !strings.Contains(got, "Alice (@alice)") || !strings.Contains(got, "...") {
			t.Fatalf("caption = %q", got)
		}
	})

	t.Run("user text dropped before tweet text", func(t *testing.T) {
		userText := strings.Repeat("u", 200)
		got := buildXPostCaption("alice", userText, "Alice", "alice", "Hello world", canonical, 150)
		if strings.Contains(got, userText) {
			t.Fatalf("user text should have been dropped: %q", got)
		}
		if !strings.Contains(got, "Hello world") || !strings.HasSuffix(got, canonical) {
			t.Fatalf("caption = %q", got)
		}
	})

	t.Run("surrogate pairs counted in utf16 units", func(t *testing.T) {
		emoji := strings.Repeat("😀", 700)
		got := buildXPostCaption("alice", "", "Alice", "alice", emoji, canonical, telegoCaptionLimit)
		if utf16Len(got) > telegoCaptionLimit {
			t.Fatalf("caption length = %d, want <= %d", utf16Len(got), telegoCaptionLimit)
		}
		if !strings.HasSuffix(got, canonical) {
			t.Fatalf("caption = %q", got)
		}
	})
}

func TestStripXPostLinks(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "only link", in: "https://x.com/alice/status/123456", want: ""},
		{name: "link with tracking param", in: "https://x.com/alice/status/123456?s=20", want: ""},
		{name: "surrounding words", in: "look https://x.com/alice/status/123456 now", want: "look  now"},
		{name: "newlines preserved", in: "line1\nhttps://x.com/a/status/1\nline2", want: "line1\n\nline2"},
		{name: "other links untouched", in: "watch https://youtu.be/ID?si=1", want: "watch https://youtu.be/ID?si=1"},
		{name: "multiple links", in: "https://x.com/a/status/1 mid https://twitter.com/b/status/2", want: "mid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripXPostLinks(tt.in); got != tt.want {
				t.Fatalf("stripXPostLinks(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// --- Pipeline -----------------------------------------------------------------

func TestProcessXPostVideoRepostDeletesOriginal(t *testing.T) {
	media, requested := xpostMediaServer(t)
	client := xpostInsecureClient()

	payload := fmt.Sprintf(`{"code":200,"tweet":{"url":"https://x.com/alice/status/123456",`+
		`"text":"Hello world","author":{"name":"Alice","screen_name":"alice"},"media":{"photos":[],`+
		`"videos":[{"url":"%s/fallback.mp4","duration":30,"formats":[`+
		`{"url":"%s/huge.mp4","bitrate":20000000,"container":"mp4"},`+
		`{"url":"%s/playlist.m3u8","container":"m3u8"},`+
		`{"url":"%s/fits.mp4","bitrate":500000,"container":"mp4"}]}]}}}`,
		media.URL, media.URL, media.URL, media.URL)
	api := xpostAPIServer(t, payload)
	defer api.Close()

	snd := &recYTSender{}
	owners := &recOwners{}
	msg := ytTestMessage("https://x.com/alice/status/123456")
	processXPost(context.Background(), snd, discardLogger(), client, api.URL, nil, owners, msg, msg.Text)

	if len(snd.Videos) != 1 || len(snd.MediaGroups) != 0 || len(snd.Messages) != 0 {
		t.Fatalf("calls: videos=%d groups=%d messages=%d", len(snd.Videos), len(snd.MediaGroups), len(snd.Messages))
	}
	video := snd.Videos[0]
	if video.ChatID.ID != msg.Chat.ID {
		t.Fatalf("video chat = %d, want %d", video.ChatID.ID, msg.Chat.ID)
	}
	if filepath.Base(video.Video.File.Name()) != "x-video-1.mp4" {
		t.Fatalf("video file = %q", filepath.Base(video.Video.File.Name()))
	}
	for _, want := range []string{"👤 alice", "Alice (@alice)", "Hello world", "https://x.com/alice/status/123456"} {
		if !strings.Contains(video.Caption, want) {
			t.Fatalf("caption %q missing %q", video.Caption, want)
		}
	}
	// The 20Mbps x 30s variant exceeds the upload budget; the fitting
	// one must have been downloaded.
	if len(*requested) != 1 || (*requested)[0] != "/fits.mp4" {
		t.Fatalf("media requests = %q, want [/fits.mp4]", *requested)
	}
	if calls := owners.recorded(); len(calls) != 1 || calls[0] != fmt.Sprintf("%d:1002:%d", msg.Chat.ID, msg.From.ID) {
		t.Fatalf("owner calls = %v", calls)
	}
	if len(snd.Deletes) != 1 || snd.Deletes[0].MessageID != msg.MessageID {
		t.Fatalf("deletes = %+v", snd.Deletes)
	}
}

func TestProcessXPostAlbumPhotosAndVideo(t *testing.T) {
	media, _ := xpostMediaServer(t)
	client := xpostInsecureClient()

	payload := fmt.Sprintf(`{"code":200,"tweet":{"url":"https://x.com/alice/status/123456",`+
		`"text":"Album tweet","author":{"name":"Alice","screen_name":"alice"},"media":{`+
		`"photos":[{"url":"%s/1.jpg"},{"url":"%s/2.jpg"}],`+
		`"videos":[{"url":"%s/v.mp4","duration":10,"formats":[`+
		`{"url":"%s/v.mp4","bitrate":1000,"container":"mp4"}]}]}}}`,
		media.URL, media.URL, media.URL, media.URL)
	api := xpostAPIServer(t, payload)
	defer api.Close()

	snd := &recYTSender{}
	msg := ytTestMessage("https://x.com/alice/status/123456")
	processXPost(context.Background(), snd, discardLogger(), client, api.URL, nil, nil, msg, msg.Text)

	if len(snd.MediaGroups) != 1 || len(snd.Videos) != 0 || len(snd.Messages) != 0 {
		t.Fatalf("calls: groups=%d videos=%d messages=%d", len(snd.MediaGroups), len(snd.Videos), len(snd.Messages))
	}
	group := snd.MediaGroups[0]
	if group.ChatID.ID != msg.Chat.ID || len(group.Media) != 3 {
		t.Fatalf("group chat/items = %d/%d", group.ChatID.ID, len(group.Media))
	}
	first, ok := group.Media[0].(*telego.InputMediaPhoto)
	if !ok || first.Media.URL != media.URL+"/1.jpg" {
		t.Fatalf("first item = %#v", group.Media[0])
	}
	if !strings.Contains(first.Caption, "Album tweet") || !strings.HasSuffix(first.Caption, "https://x.com/alice/status/123456") {
		t.Fatalf("caption = %q", first.Caption)
	}
	second := group.Media[1].(*telego.InputMediaPhoto)
	if second.Caption != "" || second.Media.URL != media.URL+"/2.jpg" {
		t.Fatalf("second item = %#v", second)
	}
	third, ok := group.Media[2].(*telego.InputMediaVideo)
	if !ok || third.Media.File == nil {
		t.Fatalf("third item = %#v", group.Media[2])
	}
	if len(snd.Deletes) != 1 {
		t.Fatalf("deletes = %+v", snd.Deletes)
	}
}

func TestProcessXPostUserPhotoKeptFirstInAlbum(t *testing.T) {
	payload := `{"code":200,"tweet":{"url":"https://x.com/alice/status/123456",` +
		`"text":"with user photo","author":{"name":"Alice","screen_name":"alice"},` +
		`"media":{"photos":[{"url":"https://pbs.twimg.com/media/1.jpg"}],"videos":[]}}}`
	api := xpostAPIServer(t, payload)
	defer api.Close()

	snd := &recYTSender{}
	msg := ytTestMessage("")
	msg.Caption = "https://x.com/alice/status/123456"
	msg.Photo = []telego.PhotoSize{
		{FileID: "small", Width: 100, Height: 100},
		{FileID: "large", Width: 800, Height: 600},
	}
	processXPost(context.Background(), snd, discardLogger(), api.Client(), api.URL, nil, nil, msg, msg.Caption)

	if len(snd.MediaGroups) != 1 || len(snd.MediaGroups[0].Media) != 2 {
		t.Fatalf("groups = %+v", snd.MediaGroups)
	}
	first := snd.MediaGroups[0].Media[0].(*telego.InputMediaPhoto)
	if first.Media.FileID != "large" {
		t.Fatalf("first item file_id = %q, want large", first.Media.FileID)
	}
	if !strings.Contains(first.Caption, "with user photo") {
		t.Fatalf("caption = %q", first.Caption)
	}
}

func TestProcessXPostTextOnlyTweet(t *testing.T) {
	payload := `{"code":200,"tweet":{"url":"https://x.com/alice/status/123456",` +
		`"text":"Just words","author":{"name":"Alice","screen_name":"alice"},"media":{}}}`
	api := xpostAPIServer(t, payload)
	defer api.Close()

	snd := &recYTSender{}
	msg := ytTestMessage("https://x.com/alice/status/123456")
	processXPost(context.Background(), snd, discardLogger(), api.Client(), api.URL, nil, nil, msg, msg.Text)

	if len(snd.Messages) != 1 || len(snd.MediaGroups) != 0 || len(snd.Videos) != 0 {
		t.Fatalf("calls: messages=%d groups=%d videos=%d", len(snd.Messages), len(snd.MediaGroups), len(snd.Videos))
	}
	if !strings.Contains(snd.Messages[0].Text, "Just words") ||
		!strings.HasSuffix(snd.Messages[0].Text, "https://x.com/alice/status/123456") {
		t.Fatalf("message = %q", snd.Messages[0].Text)
	}
	if len(snd.Deletes) != 1 {
		t.Fatalf("deletes = %+v", snd.Deletes)
	}
}

// The reported production failure: a 1080p/10Mbps minute-long clip whose
// only variants all exceed the upload cap. The repost must degrade to
// text + link instead of declining the whole post.
func TestProcessXPostOversizedVideoDegradesToText(t *testing.T) {
	media, requested := xpostMediaServer(t)
	payload := fmt.Sprintf(`{"code":200,"tweet":{"url":"https://x.com/alice/status/123456",`+
		`"text":"Big video tweet","author":{"name":"Alice","screen_name":"alice"},"media":{"photos":[],`+
		`"videos":[{"url":"%s/huge.mp4","duration":51,"formats":[`+
		`{"url":"%s/1080.mp4","bitrate":10368000,"container":"mp4"},`+
		`{"url":"%s/720.mp4","bitrate":10000000,"container":"mp4"}]}]}}}`,
		media.URL, media.URL, media.URL)
	api := xpostAPIServer(t, payload)
	defer api.Close()

	snd := &recYTSender{}
	msg := ytTestMessage("https://x.com/alice/status/123456")
	processXPost(context.Background(), snd, discardLogger(), api.Client(), api.URL, nil, nil, msg, msg.Text)

	if len(snd.Messages) != 1 || len(snd.MediaGroups) != 0 || len(snd.Videos) != 0 {
		t.Fatalf("calls: messages=%d groups=%d videos=%d", len(snd.Messages), len(snd.MediaGroups), len(snd.Videos))
	}
	if !strings.Contains(snd.Messages[0].Text, "Big video tweet") ||
		!strings.HasSuffix(snd.Messages[0].Text, "https://x.com/alice/status/123456") {
		t.Fatalf("message = %q", snd.Messages[0].Text)
	}
	if len(*requested) != 0 {
		t.Fatalf("no media should have been downloaded, got %q", *requested)
	}
	if len(snd.Deletes) != 1 {
		t.Fatalf("deletes = %+v", snd.Deletes)
	}
}

func TestProcessXPostMetadataFailureDeclinesKeepsOriginal(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer api.Close()

	snd := &recYTSender{}
	msg := ytTestMessage("https://x.com/alice/status/123456")
	processXPost(context.Background(), snd, discardLogger(), api.Client(), api.URL, nil, nil, msg, msg.Text)

	if len(snd.Messages) != 1 {
		t.Fatalf("messages = %d, want 1 decline", len(snd.Messages))
	}
	decline := snd.Messages[0]
	if !slices.Contains(publicPureFailures, decline.Text) {
		t.Fatalf("decline %q is not catalog-approved", decline.Text)
	}
	if decline.ReplyParameters == nil || decline.ReplyParameters.MessageID != msg.MessageID {
		t.Fatalf("decline reply = %#v", decline.ReplyParameters)
	}
	if len(snd.Deletes) != 0 {
		t.Fatalf("original must be kept, deletes = %+v", snd.Deletes)
	}
}

func TestProcessXPostAlbumFallbackDownloadsPhotos(t *testing.T) {
	media, _ := xpostMediaServer(t)
	client := xpostInsecureClient()

	payload := fmt.Sprintf(`{"code":200,"tweet":{"url":"https://x.com/alice/status/123456",`+
		`"text":"Album","author":{"name":"Alice","screen_name":"alice"},"media":{`+
		`"photos":[{"url":"%s/1.jpg"},{"url":"%s/2.jpg"}],"videos":[]}}}`, media.URL, media.URL)
	api := xpostAPIServer(t, payload)
	defer api.Close()

	snd := &recYTSender{GroupErr: errors.New("group send failed")}
	msg := ytTestMessage("https://x.com/alice/status/123456")
	processXPost(context.Background(), snd, discardLogger(), client, api.URL, nil, nil, msg, msg.Text)

	if len(snd.MediaGroups) != 2 {
		t.Fatalf("group attempts = %d, want 2 (URL then downloaded files)", len(snd.MediaGroups))
	}
	first, ok := snd.MediaGroups[0].Media[0].(*telego.InputMediaPhoto)
	if !ok || first.Media.URL == "" {
		t.Fatalf("first attempt should pass photo URLs, got %#v", snd.MediaGroups[0].Media[0])
	}
	second, ok := snd.MediaGroups[1].Media[0].(*telego.InputMediaPhoto)
	if !ok || second.Media.File == nil || second.Media.URL != "" {
		t.Fatalf("second attempt should upload downloaded files, got %#v", snd.MediaGroups[1].Media[0])
	}
	if len(snd.Messages) != 1 || !slices.Contains(publicPureFailures, snd.Messages[0].Text) {
		t.Fatalf("expected one decline, messages = %+v", snd.Messages)
	}
	if len(snd.Deletes) != 0 {
		t.Fatalf("original must be kept, deletes = %+v", snd.Deletes)
	}
}

func TestProcessXPostSinglePhotoFallback(t *testing.T) {
	media, _ := xpostMediaServer(t)
	client := xpostInsecureClient()

	payload := fmt.Sprintf(`{"code":200,"tweet":{"url":"https://x.com/alice/status/123456",`+
		`"text":"One photo","author":{"name":"Alice","screen_name":"alice"},"media":{`+
		`"photos":[{"url":"%s/1.jpg"}],"videos":[]}}}`, media.URL)
	api := xpostAPIServer(t, payload)
	defer api.Close()

	snd := &failingPhotoSender{}
	msg := ytTestMessage("https://x.com/alice/status/123456")
	processXPost(context.Background(), snd, discardLogger(), client, api.URL, nil, nil, msg, msg.Text)

	if len(snd.Photos) != 2 {
		t.Fatalf("photo attempts = %d, want 2", len(snd.Photos))
	}
	if snd.Photos[0].Photo.URL != media.URL+"/1.jpg" {
		t.Fatalf("first attempt should pass the URL, got %#v", snd.Photos[0].Photo)
	}
	if snd.Photos[1].Photo.File == nil {
		t.Fatalf("second attempt should upload the download, got %#v", snd.Photos[1].Photo)
	}
	if len(snd.Deletes) != 0 {
		t.Fatalf("original must be kept, deletes = %+v", snd.Deletes)
	}
}

func TestProcessXPostAlbumTruncatedToTelegramLimit(t *testing.T) {
	photos := make([]string, 12)
	for i := range photos {
		photos[i] = fmt.Sprintf(`{"url":"https://pbs.twimg.com/media/%d.jpg"}`, i+1)
	}
	payload := `{"code":200,"tweet":{"url":"https://x.com/alice/status/123456",` +
		`"text":"Many photos","author":{"name":"Alice","screen_name":"alice"},"media":{` +
		`"photos":[` + strings.Join(photos, ",") + `],"videos":[]}}}`
	api := xpostAPIServer(t, payload)
	defer api.Close()

	snd := &recYTSender{}
	msg := ytTestMessage("https://x.com/alice/status/123456")
	processXPost(context.Background(), snd, discardLogger(), api.Client(), api.URL, nil, nil, msg, msg.Text)

	if len(snd.MediaGroups) != 1 || len(snd.MediaGroups[0].Media) != xpostMaxAlbumItems {
		t.Fatalf("group items = %d, want %d", len(snd.MediaGroups[0].Media), xpostMaxAlbumItems)
	}
}

func TestProcessXPostLogsDeclineSendFailure(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "failed", http.StatusBadGateway)
	}))
	defer api.Close()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	snd := &failingXPostSender{}
	msg := ytTestMessage("https://x.com/alice/status/123456")
	processXPost(context.Background(), snd, logger, api.Client(), api.URL, nil, nil, msg, msg.Text)

	output := logs.String()
	if !strings.Contains(output, `msg="xpost: decline note send failed"`) ||
		!strings.Contains(output, "message_id=42") ||
		strings.Contains(output, "tiktok:") {
		t.Fatalf("unexpected decline failure log: %s", output)
	}
}

func TestProcessXPostTranslatesForeignTweet(t *testing.T) {
	payload := `{"code":200,"tweet":{"url":"https://x.com/alice/status/123456",` +
		`"text":"GLM5.3 重新夺回国模","lang":"zh",` +
		`"author":{"name":"Alice","screen_name":"alice"},"media":{}}}`
	api := xpostAPIServer(t, payload)
	defer api.Close()

	var gotInput string
	translate := func(_ context.Context, text string) (string, error) {
		gotInput = text
		return "GLM5.3 снова забрал корону", nil
	}
	snd := &recYTSender{}
	msg := ytTestMessage("https://x.com/alice/status/123456")
	processXPost(context.Background(), snd, discardLogger(), api.Client(), api.URL, translate, nil, msg, msg.Text)

	if gotInput != "GLM5.3 重新夺回国模" {
		t.Fatalf("translator input = %q", gotInput)
	}
	if len(snd.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(snd.Messages))
	}
	if !strings.Contains(snd.Messages[0].Text, "GLM5.3 снова забрал корону") {
		t.Fatalf("caption missing translation: %q", snd.Messages[0].Text)
	}
	if strings.Contains(snd.Messages[0].Text, "重新夺回") {
		t.Fatalf("caption must not carry the untranslated original: %q", snd.Messages[0].Text)
	}
}

func TestProcessXPostKeepsRussianAndEnglishText(t *testing.T) {
	for _, lang := range []string{"ru", "en", "und", ""} {
		t.Run("lang_"+lang, func(t *testing.T) {
			payload := fmt.Sprintf(`{"code":200,"tweet":{"url":"https://x.com/alice/status/123456",`+
				`"text":"hello мир","lang":%q,`+
				`"author":{"name":"Alice","screen_name":"alice"},"media":{}}}`, lang)
			api := xpostAPIServer(t, payload)
			defer api.Close()

			translate := func(context.Context, string) (string, error) {
				t.Fatal("translator must not run for ru/en/und tweets")
				return "", nil
			}
			snd := &recYTSender{}
			msg := ytTestMessage("https://x.com/alice/status/123456")
			processXPost(context.Background(), snd, discardLogger(), api.Client(), api.URL, translate, nil, msg, msg.Text)

			if len(snd.Messages) != 1 || !strings.Contains(snd.Messages[0].Text, "hello мир") {
				t.Fatalf("original text must be kept, got %+v", snd.Messages)
			}
		})
	}
}

func TestProcessXPostTranslationFailureKeepsOriginal(t *testing.T) {
	payload := `{"code":200,"tweet":{"url":"https://x.com/alice/status/123456",` +
		`"text":"こんにちは","lang":"ja",` +
		`"author":{"name":"Alice","screen_name":"alice"},"media":{}}}`
	api := xpostAPIServer(t, payload)
	defer api.Close()

	translate := func(context.Context, string) (string, error) {
		return "", errors.New("provider down")
	}
	snd := &recYTSender{}
	msg := ytTestMessage("https://x.com/alice/status/123456")
	processXPost(context.Background(), snd, discardLogger(), api.Client(), api.URL, translate, nil, msg, msg.Text)

	if len(snd.Messages) != 1 || !strings.Contains(snd.Messages[0].Text, "こんにちは") {
		t.Fatalf("failed translation must keep the original, got %+v", snd.Messages)
	}
	if len(snd.Deletes) != 1 {
		t.Fatalf("repost must still complete, deletes = %+v", snd.Deletes)
	}
}
