package bot

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestTikTokPhotoSoundtrackMetadata(t *testing.T) {
	for _, tc := range []struct{ name, fields, want, title, performer string }{
		{"music_info", `"play":"https://cdn.example/play.mp3","music":"https://cdn.example/music.mp3","music_info":{"play":"https://cdn.example/original.mp3","title":"Original sound","author":"Creator"}`, "https://cdn.example/original.mp3", "Original sound", "Creator"},
		{"music", `"music":"https://cdn.example/music.mp3","play":"https://cdn.example/play.mp3"`, "https://cdn.example/music.mp3", "", ""},
		{"play", `"play":"https://cdn.example/play.mp3"`, "https://cdn.example/play.mp3", "", ""},
		{"no_music", `"play":""`, "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withTikwmMediaServer(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
				_, _ = w.Write([]byte(`{"code":0,"data":{"images":["https://cdn.example/photo.jpg"],` + tc.fields + `}}`))
			})
			post, err := downloadFromMirror(context.Background(), srv0(), "https://www.tiktok.com/@user/photo/123", t.TempDir())
			if err != nil || post.AudioURL != tc.want || post.AudioTitle != tc.title || post.AudioPerformer != tc.performer || post.VideoPath != "" {
				t.Fatalf("post=%+v err=%v", post, err)
			}
		})
	}
}

type soundtrackStream struct{}

func (soundtrackStream) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestDownloadTikTokAudioBoundsStreamAndRemovesPartial(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = io.CopyN(w, soundtrackStream{}, maxVideoSize+1)
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "soundtrack.mp3")
	err := downloadTikTokAudio(context.Background(), srv.Client(), srv.URL+"/sound.mp3", path)
	if !errors.Is(err, errTikTokAudioTooLarge) {
		t.Fatalf("expected bounded audio error, got %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("partial oversized audio left on disk: %v", err)
	}
}
