package tgclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mymmrac/telego"
	"github.com/mymmrac/telego/telegoapi"
)

type slideshowCaller struct {
	t          *testing.T
	calls      int
	chatIDs    []string
	payloads   []map[string]string
	fail       error
	apiFailure bool
}

func (c *slideshowCaller) Call(_ context.Context, endpoint string, data *telegoapi.RequestData) (*telegoapi.Response, error) {
	c.t.Helper()
	if !strings.HasSuffix(endpoint, "/sendRichMessage") {
		c.t.Fatalf("wrong method: %s", endpoint)
	}
	_, params, err := mime.ParseMediaType(data.ContentType)
	if err != nil {
		return nil, err
	}
	r := multipart.NewReader(data.BodyStream, params["boundary"])
	parts := map[string]string{}
	for {
		p, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(p)
		if err != nil {
			return nil, err
		}
		parts[p.FormName()] = string(b)
	}
	c.calls++
	c.chatIDs = append(c.chatIDs, parts["chat_id"])
	c.payloads = append(c.payloads, parts)
	if c.calls == 1 && c.fail != nil {
		if c.apiFailure {
			var apiErr *telegoapi.Error
			if errors.As(c.fail, &apiErr) {
				return &telegoapi.Response{Error: apiErr}, nil
			}
		}
		return nil, c.fail
	}
	return &telegoapi.Response{Ok: true, Result: json.RawMessage(`{"message_id":321,"chat":{"id":-100,"type":"supergroup"}}`)}, nil
}

func TestSendSlideshowUploadsOrderedPhotosAndRewindsOnMigration(t *testing.T) {
	caller := &slideshowCaller{t: t, fail: migrateErr(-100999)}
	c := newClientWithCaller(t, caller)
	c.richCaller = caller
	var photos []telego.InputFile
	for i, content := range []string{"first-photo", "second-photo"} {
		path := filepath.Join(t.TempDir(), string(rune('a'+i))+".jpg")
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		photos = append(photos, telego.InputFile{File: f})
	}
	sent, err := c.SendSlideshow(context.Background(), &SlideshowParams{ChatID: telego.ChatID{ID: -42}, MessageThreadID: 7, Photos: photos, CaptionHTML: "<b>Alice</b>\n&lt;hello&gt;"})
	if err != nil || sent.MessageID != 321 {
		t.Fatalf("sent=%+v err=%v", sent, err)
	}
	if len(caller.chatIDs) != 2 || caller.chatIDs[0] != "-42" || caller.chatIDs[1] != "-100999" {
		t.Fatalf("migration lost: %v", caller.chatIDs)
	}
	for _, parts := range caller.payloads {
		if parts["photo_0"] != "first-photo" || parts["photo_1"] != "second-photo" || parts["message_thread_id"] != "7" {
			t.Fatalf("upload lost bytes/topic: %v", parts)
		}
		var rich struct {
			HTML  string `json:"html"`
			Media []struct {
				ID    string `json:"id"`
				Media struct {
					Type  string `json:"type"`
					Media string `json:"media"`
				} `json:"media"`
			} `json:"media"`
		}
		if err := json.Unmarshal([]byte(parts["rich_message"]), &rich); err != nil {
			t.Fatal(err)
		}
		if rich.HTML != "<p><b>Alice</b><br>&lt;hello&gt;</p><tg-slideshow><img src=\"tg://photo?id=photo_0\"/><img src=\"tg://photo?id=photo_1\"/></tg-slideshow>" {
			t.Fatalf("wrong slideshow HTML: %s", rich.HTML)
		}
		if len(rich.Media) != 2 || rich.Media[0].ID != "photo_0" || rich.Media[0].Media.Type != "photo" || rich.Media[0].Media.Media != "attach://photo_0" || rich.Media[1].Media.Media != "attach://photo_1" {
			t.Fatalf("wrong media references: %+v", rich.Media)
		}
	}
}

func TestSendSlideshowPreservesAPIRejection(t *testing.T) {
	caller := &slideshowCaller{t: t, fail: &telegoapi.Error{ErrorCode: 400, Description: "unsupported rich message"}, apiFailure: true}
	c := newClientWithCaller(t, caller)
	c.richCaller = caller
	_, err := c.SendSlideshow(context.Background(), &SlideshowParams{ChatID: telego.ChatID{ID: -100}, Photos: []telego.InputFile{{URL: "https://example.com/a.jpg"}}})
	var apiErr *telegoapi.Error
	if !errors.As(err, &apiErr) || apiErr.ErrorCode != 400 || caller.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, caller.calls)
	}
}

func TestSendSlideshowRedactsTokenAndPreservesCause(t *testing.T) {
	caller := &slideshowCaller{t: t}
	c := newClientWithCaller(t, caller)
	c.richCaller = caller
	cause := errors.New("Post https://api.telegram.org/bot" + c.bot.Token() + "/sendRichMessage: context canceled")
	caller.fail = fmt.Errorf("%s: %w", cause, context.Canceled)
	_, err := c.SendSlideshow(context.Background(), &SlideshowParams{ChatID: telego.ChatID{ID: -100}, Photos: []telego.InputFile{{URL: "https://example.com/a.jpg"}}})
	if err == nil || strings.Contains(err.Error(), c.bot.Token()) || !errors.Is(err, context.Canceled) {
		t.Fatalf("token redaction or error classification lost: %v", err)
	}
}

func TestSendSlideshowAllowsAutomaticLinkDetection(t *testing.T) {
	caller := &slideshowCaller{t: t}
	c := newClientWithCaller(t, caller)
	c.richCaller = caller
	_, err := c.SendSlideshow(context.Background(), &SlideshowParams{
		ChatID:      telego.ChatID{ID: -100},
		Photos:      []telego.InputFile{{URL: "https://example.com/photo.jpg"}},
		CaptionHTML: "<b>Alice</b>\nhttps://vt.tiktok.com/ZSgFJQKwT/ @example #photos",
	})
	if err != nil {
		t.Fatal(err)
	}
	var rich struct {
		SkipEntityDetection bool `json:"skip_entity_detection"`
	}
	if err := json.Unmarshal([]byte(caller.payloads[0]["rich_message"]), &rich); err != nil {
		t.Fatal(err)
	}
	if rich.SkipEntityDetection {
		t.Fatal("slideshow disables Telegram URL, mention and hashtag detection")
	}
}
