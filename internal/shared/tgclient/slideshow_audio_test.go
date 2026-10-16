package tgclient

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mymmrac/telego"
)

func slideshowUpload(t *testing.T, name, body string) telego.InputFile {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return telego.InputFile{File: f}
}

func TestSendSlideshowAudioSharesMessageAndRewinds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
	}{
		{"migration", migrateErr(-100999)},
		{"transport", context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caller := &slideshowCaller{t: t, fail: tc.failure}
			c := newClientWithCaller(t, caller)
			c.richCaller = caller
			_, err := c.SendSlideshow(context.Background(), &SlideshowParams{
				ChatID: telego.ChatID{ID: -42},
				Photos: []telego.InputFile{slideshowUpload(t, "a.jpg", "first-photo"), slideshowUpload(t, "b.jpg", "second-photo")},
				Audio:  &telego.InputMediaAudio{Media: slideshowUpload(t, "sound.mp3", "original-soundtrack"), Title: "Original <sound>", Performer: "Creator"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if caller.calls != 2 {
				t.Fatalf("attempts=%d, want two", caller.calls)
			}
			for _, parts := range caller.payloads {
				if parts["photo_0"] != "first-photo" || parts["photo_1"] != "second-photo" || parts["soundtrack"] != "original-soundtrack" {
					t.Fatalf("lost retry bytes: %+v", parts)
				}
				var rich struct {
					HTML  string `json:"html"`
					Media []struct {
						ID    string `json:"id"`
						Media struct {
							Type      string `json:"type"`
							Media     string `json:"media"`
							Title     string `json:"title"`
							Performer string `json:"performer"`
						} `json:"media"`
					} `json:"media"`
				}
				if err := json.Unmarshal([]byte(parts["rich_message"]), &rich); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(rich.HTML, `</tg-slideshow><audio src="tg://audio?id=soundtrack"></audio>`) {
					t.Fatalf("audio must be beside the slideshow in the same message: %s", rich.HTML)
				}
				if len(rich.Media) != 3 || rich.Media[2].ID != "soundtrack" || rich.Media[2].Media.Type != "audio" || rich.Media[2].Media.Media != "attach://soundtrack" || rich.Media[2].Media.Title != "Original <sound>" || rich.Media[2].Media.Performer != "Creator" {
					t.Fatalf("wrong audio upload reference or metadata: %+v", rich.Media)
				}
			}
		})
	}
}

func TestSendSlideshowCountsAudioAgainstAttachmentLimit(t *testing.T) {
	for _, count := range []int{49, 50} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			caller := &slideshowCaller{t: t}
			c := newClientWithCaller(t, caller)
			c.richCaller = caller
			photos := make([]telego.InputFile, count)
			for i := range photos {
				photos[i] = telego.InputFile{URL: "https://example.com/photo.jpg"}
			}
			_, err := c.SendSlideshow(context.Background(), &SlideshowParams{ChatID: telego.ChatID{ID: -100}, Photos: photos, Audio: &telego.InputMediaAudio{Media: telego.InputFile{URL: "https://example.com/sound.mp3"}}})
			if count == 49 && (err != nil || caller.calls != 1) {
				t.Fatalf("49 photos + audio should fit: err=%v calls=%d", err, caller.calls)
			}
			if count == 50 && (err == nil || caller.calls != 0) {
				t.Fatalf("50 photos + audio must not be truncated or sent: err=%v calls=%d", err, caller.calls)
			}
		})
	}
}
