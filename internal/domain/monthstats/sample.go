package monthstats

import (
	"time"
	"unicode/utf8"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/shared"
)

// Sample is one counted message reduced to exactly the dimensions the
// legacy chat-export.org report needs. It is produced two ways - from a
// live telego.Message via ExtractSample, and from an export row by the
// history importer - and both must produce identical numbers, so all the
// counting primitives (RuneLen, AddEntityType, CountKeyword) live here
// and are shared by both paths.
type Sample struct {
	AbsChatID   int64
	UserID      int64
	TS          time.Time // UTC
	Month       string    // TS.Format("2006-01")
	Runes       int64     // rune length of text + caption (true length, for ranking)
	CustomEmoji int64
	Code        int64
	Mention     int64
	BotCommand  int64
	Keyword     int64
	// Excerpt is the message body truncated to LongestExcerptRunes, kept
	// only so the "longest message" nomination can show text without the
	// buffer/DB ever holding a multi-thousand-rune message. ExcerptFull is
	// false when the body was cut. The longest-message ranking uses Runes
	// (the true length), not the excerpt.
	Excerpt     string
	ExcerptFull bool
}

// RuneLen counts Unicode code points, matching the legacy Clojure
// `(count s)` semantics for the BMP. (Clojure strings are UTF-16, so an
// astral char counts as 2 there and 1 here; this differs only for
// emoji-heavy messages in the longest-message ranking and is documented
// in the stats spec.)
func RuneLen(s string) int64 { return int64(utf8.RuneCountInString(s)) }

// AddEntityType increments the matching nomination counter for a Bot API
// MessageEntity .Type. The export's text_entities[].type uses the same
// vocabulary, so the importer calls this with the same strings and gets
// the same totals. Only the four legacy nominations are tracked; the
// legacy code counted entity type "code" (not "pre") and "mention" (not
// "mention_name"), so we match exactly.
func (s *Sample) AddEntityType(typ string) {
	switch typ {
	case "custom_emoji":
		s.CustomEmoji++
	case "code":
		s.Code++
	case "mention":
		s.Mention++
	case "bot_command":
		s.BotCommand++
	}
}

// Excerpt truncates s to LongestExcerptRunes runes. ok is false when the
// text was cut, so the renderer can mark it. Used at ingest time so the
// buffer/DB never hold a multi-thousand-rune message.
func Excerpt(s string) (text string, full bool) {
	r := []rune(s)
	if len(r) <= LongestExcerptRunes {
		return s, true
	}
	return string(r[:LongestExcerptRunes]), false
}

// HasContent mirrors bot.hasContent: a message counts only if it carries
// some content (text or a media kind). bot.hasContent delegates here so
// the live exclusion predicate has a single definition shared with the
// importer.
func HasContent(msg *telego.Message) bool {
	return msg.Text != "" || msg.Photo != nil || msg.Video != nil ||
		msg.Document != nil || msg.Sticker != nil || msg.Voice != nil ||
		msg.VideoNote != nil || msg.Audio != nil || msg.Animation != nil ||
		msg.Poll != nil || msg.Location != nil || msg.Contact != nil
}

// ExtractSample applies the exact live counting rules (identical to
// bot.statsCountHandler's predicate): non-bot, not an anonymous admin,
// no sender_chat, has content. ok is false for an excluded message.
func ExtractSample(msg *telego.Message) (Sample, bool) {
	if msg == nil || msg.From == nil || msg.From.IsBot ||
		shared.IsAnonymousAdmin(msg.From.ID) || msg.SenderChat != nil ||
		!HasContent(msg) {
		return Sample{}, false
	}
	abs := msg.Chat.ID
	if abs < 0 {
		abs = -abs
	}
	ts := time.Unix(int64(msg.Date), 0).UTC()
	s := Sample{
		AbsChatID: abs,
		UserID:    msg.From.ID,
		TS:        ts,
		Month:     ts.Format("2006-01"),
		Runes:     RuneLen(msg.Text) + RuneLen(msg.Caption),
	}
	for i := range msg.Entities {
		s.AddEntityType(msg.Entities[i].Type)
	}
	for i := range msg.CaptionEntities {
		s.AddEntityType(msg.CaptionEntities[i].Type)
	}
	s.Keyword = int64(CountKeyword(msg.Text))
	if msg.Caption != "" {
		s.Keyword += int64(CountKeyword(msg.Caption))
	}
	// Body for the longest-message nomination: text and caption are
	// mutually exclusive in practice (a message is either text or
	// media+caption), so prefer whichever is non-empty.
	body := msg.Text
	if body == "" {
		body = msg.Caption
	}
	s.Excerpt, s.ExcerptFull = Excerpt(body)
	return s, true
}
