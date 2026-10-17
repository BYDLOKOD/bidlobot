package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	"github.com/mymmrac/telego/telegoapi"
	"github.com/veschin/bidlobot/internal/shared/tgclient"
	"github.com/veschin/bidlobot/internal/storage"
)

type photoPostSender struct {
	recYTSender
	photos  [][]byte
	audio   []byte
	uploads []string
	caption string
	thread  int
	sendErr error
}

func (s *photoPostSender) SendSlideshow(_ context.Context, p *tgclient.SlideshowParams) (*telego.Message, error) {
	s.caption, s.thread = p.CaptionHTML, p.MessageThreadID
	if p.Audio != nil {
		var err error
		s.audio, err = io.ReadAll(p.Audio.Media.File)
		if err != nil {
			return nil, err
		}
		s.uploads = append(s.uploads, p.Audio.Media.File.Name())
	}
	for _, photo := range p.Photos {
		b, err := io.ReadAll(photo.File)
		if err != nil {
			return nil, err
		}
		s.photos = append(s.photos, b)
		s.uploads = append(s.uploads, photo.File.Name())
	}
	if len(s.Deletes) != 0 {
		return nil, errors.New("original deleted before send")
	}
	if s.sendErr != nil {
		return nil, s.sendErr
	}
	return &telego.Message{MessageID: 900}, nil
}

func photoPostServer(t *testing.T, broken bool, count int, musicMode string) {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case tikwmPathVideo:
			images := []string{srv.URL + "/first.jpg"}
			for range count - 1 {
				images = append(images, srv.URL+"/second.jpg")
			}
			play := ""
			if musicMode != "" {
				play = srv.URL + "/music.mp3"
			}
			_, _ = w.Write([]byte(ttMediaJSON(play, 99, images...)))
		case "/first.jpg":
			_, _ = w.Write([]byte("first image bytes"))
		case "/second.jpg":
			if broken {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			if musicMode == "oversized" {
				_, _ = w.Write(make([]byte, tiktokCommentMediaCap+1))
				return
			}
			_, _ = w.Write([]byte("second image bytes"))
		case "/music.mp3":
			if musicMode == "broken" {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			if musicMode != "empty" {
				_, _ = w.Write([]byte("original music bytes"))
			}
		default:
			t.Errorf("unexpected media request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	oldHost, oldInterval, oldClient := tikwmAPIHost, tikwmMinInterval, tiktokCommentHTTPClient
	tikwmAPIHost, tikwmMinInterval, tiktokCommentHTTPClient = srv.URL, 0, srv.Client()
	t.Cleanup(func() { tikwmAPIHost, tikwmMinInterval, tiktokCommentHTTPClient = oldHost, oldInterval, oldClient })
	stubYtDlp(t, func(string, string) (string, error) {
		t.Error("photo post reached yt-dlp")
		return "", errors.New("not a video")
	})
}

func TestTikTokPhotoPostRepostsAllSlidesBeforeDelete(t *testing.T) {
	photoPostServer(t, false, 2, "ok")
	snd := &photoPostSender{}
	q := &fakeDeferredQueue{}
	msg := ttTestMessage("https://www.tiktok.com/@u/photo/1")
	msg.Caption = "мой <текст>"
	msg.MessageThreadID = 17
	processTikTok(context.Background(), snd, slog.New(slog.DiscardHandler), q, nil, nil, msg, msg.Text, "")
	if len(snd.photos) != 2 || string(snd.photos[0]) != "first image bytes" || string(snd.photos[1]) != "second image bytes" {
		t.Fatalf("wrong slideshow order/content: %q", snd.photos)
	}
	if string(snd.audio) != "original music bytes" {
		t.Fatalf("soundtrack lost: %q", snd.audio)
	}
	for _, path := range snd.uploads {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("temporary upload not cleaned: %s, err=%v", path, err)
		}
	}
	if !strings.Contains(snd.caption, "мой &lt;текст&gt;") || snd.thread != 17 {
		t.Fatalf("lost caption/topic: %q %d", snd.caption, snd.thread)
	}
	if len(snd.Videos) != 0 || len(q.jobs) != 0 || len(snd.Deletes) != 1 || snd.Deletes[0].MessageID != msg.MessageID {
		t.Fatalf("video=%d queue=%d deletes=%+v", len(snd.Videos), len(q.jobs), snd.Deletes)
	}
}

func TestSlideshowDownloadBudgetScalesWithAttachments(t *testing.T) {
	for _, tc := range []struct {
		attachments int
		want        time.Duration
	}{
		{0, 60 * time.Second},
		{1, 60 * time.Second},
		{2, 90 * time.Second},
		{50, 1530 * time.Second},
	} {
		if got := slideshowDownloadBudget(tc.attachments); got != tc.want {
			t.Fatalf("budget(%d) = %s, want %s", tc.attachments, got, tc.want)
		}
	}
}

func TestTikTokPhotoPostOversizedSlideIsPermanent(t *testing.T) {
	photoPostServer(t, false, 2, "oversized")
	snd := &photoPostSender{}
	q := &fakeDeferredQueue{}
	msg := ttTestMessage("https://www.tiktok.com/@u/photo/1")
	processTikTok(context.Background(), snd, slog.New(slog.DiscardHandler), q, nil, nil, msg, msg.Text, "")
	if len(q.jobs) != 0 || len(snd.Deletes) != 0 || len(snd.Messages) != 1 {
		t.Fatalf("oversized slide must be permanent: queued=%d deletes=%d notices=%d", len(q.jobs), len(snd.Deletes), len(snd.Messages))
	}

	// The flush path must drop the job instead of keeping it until the TTL.
	flush := &photoPostSender{}
	err := tryTikTokExport(context.Background(), flush, slog.New(slog.DiscardHandler), nil, nil, -100123, 42, 0, 99, "https://www.tiktok.com/@u/photo/1", "alice", "Alice", "caption")
	if err != nil || len(flush.Deletes) != 0 || len(flush.Messages) != 1 {
		t.Fatalf("oversized slide kept in queue: err=%v deletes=%d notices=%d", err, len(flush.Deletes), len(flush.Messages))
	}
}

func TestTikTokPhotoPostFailureKeepsOriginal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		broken  bool
		sendErr error
		queued  int
	}{
		{"download", true, nil, 1},
		{"transport", false, errors.New("connection reset"), 1},
		{"rejected", false, &telegoapi.Error{ErrorCode: 400, Description: "bad media"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			photoPostServer(t, tc.broken, 2, "ok")
			snd := &photoPostSender{sendErr: tc.sendErr}
			q := &fakeDeferredQueue{}
			msg := ttTestMessage("https://www.tiktok.com/@u/photo/1")
			msg.Text = "мой комментарий https://www.tiktok.com/@u/photo/1"
			processTikTok(context.Background(), snd, slog.New(slog.DiscardHandler), q, nil, nil, msg, "https://www.tiktok.com/@u/photo/1", "")
			if len(snd.Deletes) != 0 || len(snd.Videos) != 0 || len(q.jobs) != tc.queued {
				t.Fatalf("deleted=%d videos=%d queued=%d", len(snd.Deletes), len(snd.Videos), len(q.jobs))
			}
			if tc.broken && len(snd.photos) != 0 {
				t.Fatal("sent an incomplete slideshow")
			}
			if tc.queued != 0 {
				var payload storage.RepostPayload
				if err := json.Unmarshal(q.jobs[0].Payload, &payload); err != nil {
					t.Fatal(err)
				}
				if payload.Caption != "мой комментарий https://www.tiktok.com/@u/photo/1" {
					t.Fatalf("queued post lost user text: %+v", payload)
				}
			}
		})
	}
}

func TestTikTokPhotoPostFlush(t *testing.T) {
	for _, count := range []int{1, 50} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			photoPostServer(t, false, count, "")
			snd := &photoPostSender{}
			owners := &recOwners{}
			err := tryTikTokExport(context.Background(), snd, slog.New(slog.DiscardHandler), owners, nil, -100123, 42, 0, 99, "https://www.tiktok.com/@u/photo/1", "alice", "Alice", "caption")
			if err != nil || len(snd.photos) != count || len(snd.Deletes) != 1 || len(snd.Videos) != 0 {
				t.Fatalf("err=%v photos=%d deletes=%d videos=%d", err, len(snd.photos), len(snd.Deletes), len(snd.Videos))
			}
			if len(owners.calls) != 1 || owners.calls[0] != "-100123:900:99" {
				t.Fatalf("lost repost ownership: %v", owners.calls)
			}
		})
	}
}

func TestTikTokPhotoPostOverLimitNeverTruncates(t *testing.T) {
	photoPostServer(t, false, 51, "")
	snd := &photoPostSender{}
	msg := ttTestMessage("https://www.tiktok.com/@u/photo/1")
	processTikTok(context.Background(), snd, slog.New(slog.DiscardHandler), nil, nil, nil, msg, msg.Text, "")
	if len(snd.photos) != 0 || len(snd.Deletes) != 0 || len(snd.Messages) != 1 {
		t.Fatalf("photos=%d deletes=%d notices=%d", len(snd.photos), len(snd.Deletes), len(snd.Messages))
	}
}

func TestTikTokPhotoPostFlushRetainsTransientFailures(t *testing.T) {
	photoPostServer(t, false, 2, "ok")
	snd := &photoPostSender{sendErr: errors.New("connection reset")}
	err := tryTikTokExport(context.Background(), snd, slog.New(slog.DiscardHandler), nil, nil, -100123, 42, 0, 99, "https://www.tiktok.com/@u/photo/1", "alice", "Alice", "caption")
	if err == nil || len(snd.Deletes) != 0 {
		t.Fatalf("failed flush must stay queued with source intact: err=%v deletes=%v", err, snd.Deletes)
	}
}

func TestTikTokPhotoPostSoundtrackFailureKeepsOriginal(t *testing.T) {
	for _, mode := range []string{"broken", "empty"} {
		t.Run(mode, func(t *testing.T) {
			photoPostServer(t, false, 2, mode)
			snd := &photoPostSender{}
			q := &fakeDeferredQueue{}
			msg := ttTestMessage("https://www.tiktok.com/@u/photo/1")
			processTikTok(context.Background(), snd, slog.New(slog.DiscardHandler), q, nil, nil, msg, msg.Text, "")
			if len(snd.Deletes) != 0 || len(snd.photos) != 0 || len(snd.Videos) != 0 || len(q.jobs) != 1 {
				t.Fatalf("failed soundtrack must retain source and queue whole post: deletes=%d photos=%d videos=%d queued=%d", len(snd.Deletes), len(snd.photos), len(snd.Videos), len(q.jobs))
			}
		})
	}
}

func TestTikTokPhotoPostFlushIncludesSoundtrack(t *testing.T) {
	photoPostServer(t, false, 2, "ok")
	snd := &photoPostSender{}
	err := tryTikTokExport(context.Background(), snd, slog.New(slog.DiscardHandler), nil, nil, -100123, 42, 0, 99, "https://www.tiktok.com/@u/photo/1", "alice", "Alice", "caption")
	if err != nil || string(snd.audio) != "original music bytes" || len(snd.photos) != 2 || len(snd.Deletes) != 1 {
		t.Fatalf("flush lost soundtrack: err=%v audio=%q photos=%d deletes=%d", err, snd.audio, len(snd.photos), len(snd.Deletes))
	}
}

func TestTikTokPhotoPostAudioOverAttachmentLimitKeepsOriginal(t *testing.T) {
	photoPostServer(t, false, 50, "ok")
	snd := &photoPostSender{}
	q := &fakeDeferredQueue{}
	msg := ttTestMessage("https://www.tiktok.com/@u/photo/1")
	processTikTok(context.Background(), snd, slog.New(slog.DiscardHandler), q, nil, nil, msg, msg.Text, "")
	if len(snd.photos) != 0 || len(snd.audio) != 0 || len(snd.Deletes) != 0 || len(q.jobs) != 0 || len(snd.Messages) != 1 {
		t.Fatalf("over-limit post must not lose media: photos=%d audio=%d deletes=%d queued=%d notices=%d", len(snd.photos), len(snd.audio), len(snd.Deletes), len(q.jobs), len(snd.Messages))
	}
}

func TestTikTokPhotoPostFlushPreservesTopic(t *testing.T) {
	photoPostServer(t, false, 2, "ok")
	snd := &photoPostSender{sendErr: errors.New("connection reset")}
	q := &fakeDeferredQueue{}
	msg := ttTestMessage("https://www.tiktok.com/@u/photo/1")
	msg.MessageThreadID = 17
	log := slog.New(slog.DiscardHandler)
	processTikTok(context.Background(), snd, log, q, nil, nil, msg, msg.Text, "")
	if len(q.jobs) != 1 || len(snd.Deletes) != 0 {
		t.Fatalf("failed send must queue without deleting source: jobs=%d deletes=%d", len(q.jobs), len(snd.Deletes))
	}
	var p storage.RepostPayload
	if err := json.Unmarshal(q.jobs[0].Payload, &p); err != nil {
		t.Fatal(err)
	}
	retry := &photoPostSender{}
	job := q.jobs[0]
	err := tryTikTokExport(context.Background(), retry, log, nil, nil,
		job.ChatID, job.MessageID, p.MessageThreadID, job.UserID, p.URL, p.Username, p.FirstName, p.Caption)
	if err != nil {
		t.Fatal(err)
	}
	if retry.thread != 17 || len(retry.Deletes) != 1 {
		t.Fatalf("flush moved photo post out of original topic: thread=%d deletes=%d", retry.thread, len(retry.Deletes))
	}
}
