package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mymmrac/telego"
)

// withTikwmMediaServer points tikwmAPIHost at a stub server and zeroes
// the pacer interval so the suite does not sleep. The handler receives
// the stub's own base URL, which is what the canned metadata body must
// use as the CDN host.
func withTikwmMediaServer(t *testing.T, h func(w http.ResponseWriter, r *http.Request, base string)) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h(w, r, srv.URL)
	}))
	t.Cleanup(srv.Close)
	prevHost, prevInterval := tikwmAPIHost, tikwmMinInterval
	tikwmAPIHost, tikwmMinInterval = srv.URL, 0
	t.Cleanup(func() { tikwmAPIHost, tikwmMinInterval = prevHost, prevInterval })
	return srv
}

// ttMediaJSON builds a tikwm media envelope.
func ttMediaJSON(play string, size int64, images ...string) string {
	var body struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Play   string   `json:"play"`
			Size   int64    `json:"size"`
			Images []string `json:"images,omitempty"`
		} `json:"data"`
	}
	body.Code = 0
	body.Msg = "success"
	body.Data.Play = play
	body.Data.Size = size
	body.Data.Images = images
	b, _ := json.Marshal(body)
	return string(b)
}

// stubYtDlp replaces the yt-dlp fallback for the duration of one test.
func stubYtDlp(t *testing.T, fn func(rawURL, workDir string) (string, error)) *atomic.Int32 {
	t.Helper()
	calls := &atomic.Int32{}
	prev := ytdlpDownload
	ytdlpDownload = func(_ context.Context, rawURL, workDir string) (string, error) {
		calls.Add(1)
		return fn(rawURL, workDir)
	}
	t.Cleanup(func() { ytdlpDownload = prev })
	return calls
}

// TestDownloadFromMirrorWritesStreamedBytes: the happy path the mirror
// exists for - metadata resolves a play URL, the CDN body lands in
// workDir byte for byte.
func TestDownloadFromMirrorWritesStreamedBytes(t *testing.T) {
	payload := make([]byte, 1024)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	var requestedURL string
	withTikwmMediaServer(t, func(w http.ResponseWriter, r *http.Request, base string) {
		switch r.URL.Path {
		case tikwmPathVideo:
			requestedURL = r.URL.Query().Get("url")
			_, _ = w.Write([]byte(ttMediaJSON(base+"/v.mp4", int64(len(payload)))))
		case "/v.mp4":
			_, _ = w.Write(payload)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	dir := t.TempDir()
	path, err := downloadFromMirror(context.Background(), srv0(), "www.tiktok.com/@u/video/1", dir)
	if err != nil {
		t.Fatalf("downloadFromMirror: %v", err)
	}
	if requestedURL != "https://www.tiktok.com/@u/video/1" {
		t.Errorf("tikwm url param = %q, want the scheme-completed video URL", requestedURL)
	}
	if filepath.Dir(path) != dir {
		t.Errorf("path %q is not inside workDir %q", path, dir)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("downloaded %d bytes, want %d identical bytes", len(got), len(payload))
	}
}

// TestDownloadFromMirrorRejectsOversizedReportedSize: a metadata size
// above the Telegram cap fails before any byte is downloaded.
func TestDownloadFromMirrorRejectsOversizedReportedSize(t *testing.T) {
	var cdnRequests atomic.Int32
	withTikwmMediaServer(t, func(w http.ResponseWriter, r *http.Request, base string) {
		if r.URL.Path == tikwmPathVideo {
			_, _ = w.Write([]byte(ttMediaJSON(base+"/v.mp4", maxVideoSize+1)))
			return
		}
		cdnRequests.Add(1)
		_, _ = w.Write([]byte("should not be fetched"))
	})

	dir := t.TempDir()
	if _, err := downloadFromMirror(context.Background(), srv0(), "https://www.tiktok.com/@u/video/1", dir); err == nil {
		t.Fatal("expected an error for a video over the cap")
	}
	if cdnRequests.Load() != 0 {
		t.Errorf("cdn was fetched %d times, want 0", cdnRequests.Load())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("workDir left with %d entries, want none", len(entries))
	}
}

// TestDownloadFromMirrorTikwmError: a tikwm refusal (free-tier limiter,
// bad URL) surfaces with the mirror's own code and message so the log
// line can be acted on.
func TestDownloadFromMirrorTikwmError(t *testing.T) {
	withTikwmMediaServer(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		_, _ = w.Write([]byte(`{"code":-1,"msg":"Free Api Limit"}`))
	})

	_, err := downloadFromMirror(context.Background(), srv0(), "https://www.tiktok.com/@u/video/1", t.TempDir())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "Free Api Limit") {
		t.Errorf("error = %v, want it to name the tikwm message", err)
	}
}

// TestDownloadFromMirrorPhotoPostIsSentinel: a post that carries an
// image list but whose play URL yields no MP4 is a photo post, and the
// error must say so - that is what stops the job from being queued
// forever. Measured shape: tikwm answers a /photo/ query with images plus
// a play URL on a music host that never serves a video.
func TestDownloadFromMirrorPhotoPostIsSentinel(t *testing.T) {
	withTikwmMediaServer(t, func(w http.ResponseWriter, r *http.Request, base string) {
		if r.URL.Path == tikwmPathVideo {
			_, _ = w.Write([]byte(ttMediaJSON(base+"/slideshow.mp4", 0, "https://cdn.example/1.jpeg")))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	_, err := downloadFromMirror(context.Background(), srv0(), "https://www.tiktok.com/@u/photo/1", t.TempDir())
	if !errors.Is(err, errNoVideo) {
		t.Fatalf("error = %v, want errNoVideo", err)
	}
}

// TestDownloadFromMirrorNoPlayURL: a response with neither a play URL nor
// images is a different failure and must not claim a photo post.
func TestDownloadFromMirrorNoPlayURL(t *testing.T) {
	withTikwmMediaServer(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{}}`))
	})

	_, err := downloadFromMirror(context.Background(), srv0(), "https://www.tiktok.com/@u/video/1", t.TempDir())
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, errNoVideo) {
		t.Fatalf("error = %v, must not be errNoVideo", err)
	}
}

// TestDownloadTikTokPrefersMirror: when the mirror answers, yt-dlp is
// never invoked - that is the whole point of the mirror, since yt-dlp is
// the path blocked by TLS-SNI filtering.
func TestDownloadTikTokPrefersMirror(t *testing.T) {
	payload := []byte("mirror bytes")
	withTikwmMediaServer(t, func(w http.ResponseWriter, r *http.Request, base string) {
		switch r.URL.Path {
		case tikwmPathVideo:
			_, _ = w.Write([]byte(ttMediaJSON(base+"/v.mp4", int64(len(payload)))))
		case "/v.mp4":
			_, _ = w.Write(payload)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	dlp := stubYtDlp(t, func(string, string) (string, error) {
		return "", errors.New("yt-dlp must not be called when the mirror answers")
	})

	path, err := downloadTikTok(context.Background(), "vt.tiktok.com/ZSq5d4Rxh/", t.TempDir())
	if err != nil {
		t.Fatalf("downloadTikTok: %v", err)
	}
	if dlp.Load() != 0 {
		t.Errorf("yt-dlp ran %d times, want 0", dlp.Load())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("bytes = %q, want %q", got, payload)
	}
}

// TestDownloadTikTokFallsBackToYtdlp: a mirror outage (500) must reach
// yt-dlp with the scheme-completed URL and return its file.
func TestDownloadTikTokFallsBackToYtdlp(t *testing.T) {
	withTikwmMediaServer(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	var seenURL string
	dlp := stubYtDlp(t, func(rawURL, workDir string) (string, error) {
		seenURL = rawURL
		path := filepath.Join(workDir, "video.mp4")
		if err := os.WriteFile(path, []byte("yt-dlp bytes"), 0o644); err != nil {
			return "", err
		}
		return path, nil
	})

	path, err := downloadTikTok(context.Background(), "vt.tiktok.com/ZSq5d4Rxh/", t.TempDir())
	if err != nil {
		t.Fatalf("downloadTikTok: %v", err)
	}
	if dlp.Load() != 1 {
		t.Fatalf("yt-dlp ran %d times, want 1", dlp.Load())
	}
	if seenURL != "vt.tiktok.com/ZSq5d4Rxh/" {
		t.Errorf("yt-dlp got %q, want the raw link (the yt-dlp path completes the scheme)", seenURL)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "yt-dlp bytes" {
		t.Errorf("bytes = %q", got)
	}
}

// TestDownloadTikTokReportsBothPaths: when both sources fail the error
// names each one, so the production log tells which half broke.
func TestDownloadTikTokReportsBothPaths(t *testing.T) {
	withTikwmMediaServer(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	stubYtDlp(t, func(string, string) (string, error) {
		return "", errors.New("no such host")
	})

	_, err := downloadTikTok(context.Background(), "vt.tiktok.com/ZSq5d4Rxh/", t.TempDir())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "mirror:") || !strings.Contains(err.Error(), "yt-dlp:") {
		t.Errorf("error = %v, want both paths named", err)
	}
}

// TestProcessTikTokPhotoPostDeclines: a photo post posted in a chat gets
// a decline note and no queue entry - the job could never succeed.
func TestProcessTikTokPhotoPostDeclines(t *testing.T) {
	withTikwmMediaServer(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"play":"","images":["https://cdn.example/1.jpeg"]}}`))
	})
	dlp := stubYtDlp(t, func(string, string) (string, error) {
		return "", errors.New("yt-dlp must not be called for a photo post")
	})

	q := &fakeDeferredQueue{}
	snd := &recYTSender{}
	log := slog.New(slog.DiscardHandler)

	msg := &telego.Message{
		MessageID: 42,
		Chat:      telego.Chat{ID: -100123, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
	}

	processTikTok(context.Background(), snd, log, q, nil, nil, msg, "https://www.tiktok.com/@u/photo/1", "")

	if len(q.jobs) != 0 {
		t.Errorf("queued %d jobs for a photo post, want 0", len(q.jobs))
	}
	if dlp.Load() != 0 {
		t.Errorf("yt-dlp ran %d times for a photo post, want 0", dlp.Load())
	}
	if len(snd.Videos) != 0 {
		t.Errorf("sent %d videos for a photo post, want 0", len(snd.Videos))
	}
	if len(snd.Deletes) != 0 {
		t.Errorf("deleted the original %d times, want 0", len(snd.Deletes))
	}
	if len(snd.Messages) != 1 {
		t.Fatalf("decline messages = %d, want 1", len(snd.Messages))
	}
	if !failureCatalogContains(snd.Messages[0].Text) {
		t.Errorf("decline text must come from FailureCatalog, got %q", snd.Messages[0].Text)
	}
}

// TestPreviewOutputTruncates keeps one yt-dlp failure from dumping
// kilobytes of child-process output into a log record.
func TestPreviewOutputTruncates(t *testing.T) {
	long := strings.Repeat("x", 1000)
	got := previewOutput([]byte("  "+long+"  \n"), 400)
	if !strings.HasSuffix(got, "...(truncated)") {
		t.Fatalf("got %q", got[:20])
	}
	if len([]rune(got)) != 400+len("...(truncated)") {
		t.Fatalf("rune length = %d", len([]rune(got)))
	}
	if short := previewOutput([]byte(" boom \n"), 400); short != "boom" {
		t.Errorf("short output = %q, want trimmed", short)
	}
}
