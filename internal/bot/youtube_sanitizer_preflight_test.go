package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/mymmrac/telego"
)

// Every unrepresentable shape must name its reason, and the two safe
// shapes (plain text, plain media) must pass.
func TestPreflightReason(t *testing.T) {
	long := &sanitizedPost{text: strings.Repeat("а", 4097)}
	media := &sanitizedPost{caption: "https://youtu.be/ID"}
	cases := []struct {
		name string
		msg  *telego.Message
		plan *sanitizedPost
		want string
	}{
		{"plain text", ytTestMessage("hi"), &sanitizedPost{text: "hi"}, ""},
		{"plain media", func() *telego.Message {
			m := ytTestMessage("")
			m.Photo = []telego.PhotoSize{{FileID: "p"}}
			return m
		}(), media, ""},
		{"album item", func() *telego.Message {
			m := ytTestMessage("")
			m.MediaGroupID = "grp"
			return m
		}(), media, "album item"},
		{"paid media", func() *telego.Message {
			m := ytTestMessage("")
			m.PaidMedia = &telego.PaidMediaInfo{}
			return m
		}(), media, "paid media or paid post"},
		{"inline keyboard", func() *telego.Message {
			m := ytTestMessage("hi")
			m.ReplyMarkup = &telego.InlineKeyboardMarkup{}
			return m
		}(), &sanitizedPost{text: "hi"}, "inline keyboard not preservable"},
		{"media spoiler", func() *telego.Message {
			m := ytTestMessage("")
			m.Photo = []telego.PhotoSize{{FileID: "p"}}
			m.HasMediaSpoiler = true
			return m
		}(), media, "media spoiler not preservable"},
		{"automatic forward", func() *telego.Message {
			m := ytTestMessage("hi")
			m.IsAutomaticForward = true
			return m
		}(), &sanitizedPost{text: "hi"}, "automatic channel forward"},
		{"quote without local reply", func() *telego.Message {
			m := ytTestMessage("hi")
			m.Quote = &telego.TextQuote{Text: "hi"}
			return m
		}(), &sanitizedPost{text: "hi"}, "quote without local reply"},
		{"external reply", func() *telego.Message {
			m := ytTestMessage("hi")
			m.ExternalReply = &telego.ExternalReplyInfo{}
			return m
		}(), &sanitizedPost{text: "hi"}, "unsupported reply context"},
		{"text over message limit", ytTestMessage("hi"), long, "cleaned text over 4096 UTF-16 units"},
		{"unsupported content kind", func() *telego.Message {
			m := ytTestMessage("")
			m.Sticker = &telego.Sticker{FileID: "s"}
			return m
		}(), media, "unsupported content kind"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := preflightReason(c.msg, c.plan); got != c.want {
				t.Errorf("preflightReason = %q, want %q", got, c.want)
			}
		})
	}
}

// An album item carrying a tracked link keeps the whole album untouched:
// no sends, no deletes, no exception notices.
func TestHandleSanitizeAlbumKeptWhole(t *testing.T) {
	snd := &scriptYTImmSender{}
	msg := ytTestMessage("")
	msg.Photo = []telego.PhotoSize{{FileID: "p"}}
	msg.MediaGroupID = "grp"
	msg.Caption = "see https://youtu.be/ID?si=trk"

	act, plan := sanitizeDecision(msg)
	if !act {
		t.Fatal("expected the caption to qualify for sanitizing")
	}
	handleSanitize(context.Background(), snd, testLogger(), msg, plan)

	if len(snd.messages)+len(snd.singles)+len(snd.copies) != 0 {
		t.Error("an album item must not be copied")
	}
	if len(snd.deletes) != 0 {
		t.Error("an album item must not be deleted")
	}
}
