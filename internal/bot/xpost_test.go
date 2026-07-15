package bot

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

func TestProcessXPostScreenshotOnly(t *testing.T) {
	var screenshotWatermark, metadataURL, screenshotURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tweet":
			metadataURL = r.URL.Query().Get("url")
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"tweet":{"media":{"videos":[]}}}`)
		case "/api/screenshot":
			screenshotWatermark = r.URL.Query().Get("watermark")
			screenshotURL = r.URL.Query().Get("url")
			w.Header().Set("Content-Type", "image/png")
			_ = png.Encode(w, image.NewRGBA(image.Rect(0, 0, 1, 1)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	snd := &recYTSender{}
	msg := ytTestMessage("https://X.COM/alice/status/123456")
	processXPost(context.Background(), snd, discardLogger(), server.Client(), server.URL, msg, msg.Text)

	if len(snd.Photos) != 1 || len(snd.Videos) != 0 || len(snd.Messages) != 0 || len(snd.Deletes) != 0 {
		t.Fatalf("calls: photos=%d videos=%d messages=%d deletes=%d", len(snd.Photos), len(snd.Videos), len(snd.Messages), len(snd.Deletes))
	}
	photo := snd.Photos[0]
	if photo.ChatID.ID != msg.Chat.ID || photo.ReplyParameters == nil || photo.ReplyParameters.MessageID != msg.MessageID {
		t.Fatalf("photo target/reply = chat %d, reply %#v", photo.ChatID.ID, photo.ReplyParameters)
	}
	if photo.Caption != "" || filepath.Base(photo.Photo.File.Name()) != "x-post.png" {
		t.Fatalf("photo caption/name = %q/%q", photo.Caption, filepath.Base(photo.Photo.File.Name()))
	}
	if screenshotWatermark != "0" {
		t.Fatalf("screenshot watermark = %q, want 0", screenshotWatermark)
	}
	if metadataURL != "https://x.com/alice/status/123456" || screenshotURL != metadataURL {
		t.Fatalf("sidecar URLs = metadata %q, screenshot %q", metadataURL, screenshotURL)
	}
}

func TestProcessXPostWithVideos(t *testing.T) {
	var requestedVideos []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tweet":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"tweet":{"media":{"videos":[{"url":"https://video.twimg.com/one.mp4"},{"url":"https://video.twimg.com/two.mp4"}]}}}`)
		case "/api/screenshot":
			w.Header().Set("Content-Type", "image/png")
			_ = png.Encode(w, image.NewRGBA(image.Rect(0, 0, 1, 1)))
		case "/api/video":
			requestedVideos = append(requestedVideos, r.URL.Query().Get("url"))
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = io.WriteString(w, "small mp4")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	snd := &recYTSender{}
	msg := ytTestMessage("https://x.com/alice/status/123456")
	processXPost(context.Background(), snd, discardLogger(), server.Client(), server.URL, msg, msg.Text)

	if len(snd.Photos) != 1 || len(snd.Videos) != 2 || len(snd.Messages) != 0 || len(snd.Deletes) != 0 {
		t.Fatalf("calls: photos=%d videos=%d messages=%d deletes=%d", len(snd.Photos), len(snd.Videos), len(snd.Messages), len(snd.Deletes))
	}
	for i, video := range snd.Videos {
		wantName := "x-video-" + string(rune('1'+i)) + ".mp4"
		if filepath.Base(video.Video.File.Name()) != wantName {
			t.Errorf("video %d name = %q, want %q", i, filepath.Base(video.Video.File.Name()), wantName)
		}
		if video.ChatID.ID != msg.Chat.ID || video.ReplyParameters == nil || video.ReplyParameters.MessageID != msg.MessageID {
			t.Errorf("video %d target/reply = chat %d, reply %#v", i, video.ChatID.ID, video.ReplyParameters)
		}
	}
	wantRequested := []string{"https://video.twimg.com/one.mp4", "https://video.twimg.com/two.mp4"}
	if !slices.Equal(requestedVideos, wantRequested) {
		t.Fatalf("requested videos = %q, want %q", requestedVideos, wantRequested)
	}
}

func TestProcessXPostKeepsPartialSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tweet":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"tweet":{"media":{"videos":[{"url":"https://video.twimg.com/large.mp4"},{"url":"https://video.twimg.com/good.mp4"}]}}}`)
		case "/api/screenshot":
			http.Error(w, "failed", http.StatusInternalServerError)
		case "/api/video":
			if strings.Contains(r.URL.Query().Get("url"), "large.mp4") {
				_, _ = io.CopyN(w, zeroReader{}, int64(maxVideoSize)+1)
				return
			}
			_, _ = io.WriteString(w, "small mp4")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	snd := &recYTSender{}
	msg := ytTestMessage("https://x.com/alice/status/123456")
	client := &http.Client{Timeout: 30 * time.Second}
	processXPost(context.Background(), snd, discardLogger(), client, server.URL, msg, msg.Text)

	if len(snd.Photos) != 0 || len(snd.Videos) != 1 || len(snd.Messages) != 1 || len(snd.Deletes) != 0 {
		t.Fatalf("calls: photos=%d videos=%d messages=%d deletes=%d", len(snd.Photos), len(snd.Videos), len(snd.Messages), len(snd.Deletes))
	}
	if filepath.Base(snd.Videos[0].Video.File.Name()) != "x-video-2.mp4" {
		t.Fatalf("successful video name = %q", filepath.Base(snd.Videos[0].Video.File.Name()))
	}
	decline := snd.Messages[0]
	if decline.ReplyParameters == nil || decline.ReplyParameters.MessageID != msg.MessageID {
		t.Fatalf("decline reply = %#v", decline.ReplyParameters)
	}
	if !slices.Contains(publicPureFailures, decline.Text) {
		t.Fatalf("decline %q is not catalog-approved", decline.Text)
	}
}

func TestProcessXPostLogsDeclineSendFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "failed", http.StatusBadGateway)
	}))
	defer server.Close()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	snd := &failingXPostSender{}
	msg := ytTestMessage("https://x.com/alice/status/123456")
	processXPost(context.Background(), snd, logger, server.Client(), server.URL, msg, msg.Text)

	output := logs.String()
	if !strings.Contains(output, `msg="xpost: send failed"`) ||
		!strings.Contains(output, "message_id=42") ||
		strings.Contains(output, "tiktok:") {
		t.Fatalf("unexpected decline failure log: %s", output)
	}
}

type failingXPostSender struct {
	recYTSender
}

func (*failingXPostSender) SendMessage(context.Context, *telego.SendMessageParams) (*telego.Message, error) {
	return nil, errors.New("telegram unavailable")
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
