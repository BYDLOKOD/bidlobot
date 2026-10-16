package tgclient

// telego v1.8 predates Bot API rich messages. Keep this extension narrow:
// https://core.telegram.org/bots/api#sendrichmessage
// https://core.telegram.org/bots/api#inputrichmessagemedia

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/mymmrac/telego"
	"github.com/mymmrac/telego/telegoapi"
)

// MaxSlideshowPhotos is the rich-message media attachment limit.
const MaxSlideshowPhotos = 50

// SlideshowParams sends photos and optional music in one native rich message.
// CaptionHTML must already be escaped/formatted by the caller.
type SlideshowParams struct {
	ChatID          telego.ChatID
	MessageThreadID int
	Photos          []telego.InputFile
	Audio           *telego.InputMediaAudio
	CaptionHTML     string
}

type richMedia struct {
	ID    string `json:"id"`
	Media struct {
		Type      string `json:"type"`
		Media     string `json:"media"`
		Title     string `json:"title,omitempty"`
		Performer string `json:"performer,omitempty"`
		Duration  int    `json:"duration,omitempty"`
	} `json:"media"`
}

// SendSlideshow shares the media retry, migration and per-chat rate budget.
// Uploads stream from disk and rewind before each attempt, including migration.
func (c *Client) SendSlideshow(ctx context.Context, p *SlideshowParams) (*telego.Message, error) {
	if p == nil || len(p.Photos) == 0 {
		return nil, errors.New("tgclient: slideshow must contain photos")
	}
	attachments := len(p.Photos)
	if p.Audio != nil {
		attachments++
	}
	if attachments > MaxSlideshowPhotos {
		return nil, errors.New("tgclient: rich message exceeds 50 media attachments")
	}
	files := make(map[string]telegoapi.NamedReader, attachments)
	media := make([]richMedia, 0, attachments)
	var html strings.Builder
	if p.CaptionHTML != "" {
		html.WriteString("<p>")
		html.WriteString(strings.ReplaceAll(p.CaptionHTML, "\n", "<br>"))
		html.WriteString("</p>")
	}
	if len(p.Photos) > 1 {
		html.WriteString("<tg-slideshow>")
	}
	for i, photo := range p.Photos {
		id := "photo_" + strconv.Itoa(i)
		item := richMedia{ID: id}
		item.Media.Type = "photo"
		switch {
		case photo.File != nil:
			files[id] = photo.File
			item.Media.Media = "attach://" + id
		case photo.FileID != "":
			item.Media.Media = photo.FileID
		case photo.URL != "":
			item.Media.Media = photo.URL
		default:
			return nil, errors.New("tgclient: empty slideshow photo")
		}
		media = append(media, item)
		html.WriteString(`<img src="tg://photo?id=` + id + `"/>`)
	}
	if len(p.Photos) > 1 {
		html.WriteString("</tg-slideshow>")
	}
	if p.Audio != nil {
		item := richMedia{ID: "soundtrack"}
		item.Media.Type = "audio"
		item.Media.Title = p.Audio.Title
		item.Media.Performer = p.Audio.Performer
		item.Media.Duration = p.Audio.Duration
		switch audio := p.Audio.Media; {
		case audio.File != nil:
			files["soundtrack"] = audio.File
			item.Media.Media = "attach://soundtrack"
		case audio.FileID != "":
			item.Media.Media = audio.FileID
		case audio.URL != "":
			item.Media.Media = audio.URL
		default:
			return nil, errors.New("tgclient: empty slideshow soundtrack")
		}
		media = append(media, item)
		html.WriteString(`<audio src="tg://audio?id=soundtrack"></audio>`)
	}
	rich, err := json.Marshal(struct {
		HTML                string      `json:"html"`
		Media               []richMedia `json:"media"`
		SkipEntityDetection bool        `json:"skip_entity_detection"`
	}{html.String(), media, true})
	if err != nil {
		return nil, err
	}
	caller := c.richCaller
	if caller == nil {
		caller = telegoapi.DefaultHTTPCaller
	}
	var sent *telego.Message
	err = c.runWritePolicy(ctx, c.mediaPolicy, p.ChatID.ID, "sendRichMessage", func(ctx context.Context) error {
		for _, photo := range p.Photos {
			if err := rewindUploadBody(photo); err != nil {
				return err
			}
		}
		if p.Audio != nil {
			if err := rewindUploadBody(p.Audio.Media); err != nil {
				return err
			}
		}
		fields := map[string]string{"chat_id": p.ChatID.String(), "rich_message": string(rich)}
		if p.MessageThreadID != 0 {
			fields["message_thread_id"] = strconv.Itoa(p.MessageThreadID)
		}
		data, err := (telegoapi.DefaultConstructor{}).MultipartRequest(fields, files)
		if err != nil {
			return err
		}
		if body, ok := data.BodyStream.(io.Closer); ok {
			defer body.Close()
		}
		response, err := caller.Call(ctx, "https://api.telegram.org/bot"+c.bot.Token()+"/sendRichMessage", data)
		if err != nil {
			return &richTransportError{err, c.bot.Token()}
		}
		if response == nil {
			return errors.New("tgclient: empty rich-message response")
		}
		if !response.Ok {
			if response.Error != nil {
				return response.Error
			}
			return errors.New("tgclient: rich-message request failed without API error")
		}
		var msg telego.Message
		if err := json.Unmarshal(response.Result, &msg); err != nil {
			return fmt.Errorf("decode rich message: %w", err)
		}
		if msg.MessageID <= 0 {
			return errors.New("tgclient: rich message has no confirmed message ID")
		}
		sent = &msg
		return nil
	}, func(newSigned int64) { p.ChatID = telego.ChatID{ID: newSigned} })
	return sent, err
}

// net/http errors may include the token-bearing request URL. Preserve the
// underlying error for retry classification, but never expose the token in logs.
type richTransportError struct {
	cause error
	token string
}

func (e *richTransportError) Error() string {
	return strings.ReplaceAll(e.cause.Error(), e.token, "[REDACTED]")
}
func (e *richTransportError) Unwrap() error { return e.cause }
