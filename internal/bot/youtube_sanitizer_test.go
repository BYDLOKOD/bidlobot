package bot

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/veschin/bidlobot/internal/shared"
)

func TestStripShareTracking(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		wantOut     string
		wantChanged bool
	}{
		{
			name:        "youtu.be short with si only, no scheme",
			in:          "youtu.be/dQw4w9WgXcQ?si=abcDEF",
			wantOut:     "youtu.be/dQw4w9WgXcQ",
			wantChanged: true,
		},
		{
			name:        "youtu.be short with si only, https",
			in:          "https://youtu.be/dQw4w9WgXcQ?si=abcDEF",
			wantOut:     "https://youtu.be/dQw4w9WgXcQ",
			wantChanged: true,
		},
		{
			name:        "watch keeps v and t, drops si (order preserved)",
			in:          "https://www.youtube.com/watch?v=dQw4w9WgXcQ&si=XYZ&t=10",
			wantOut:     "https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=10",
			wantChanged: true,
		},
		{
			name:        "si in the middle, params before and after kept in order",
			in:          "https://www.youtube.com/watch?v=ID&list=PL123&si=trk&index=2",
			wantOut:     "https://www.youtube.com/watch?v=ID&list=PL123&index=2",
			wantChanged: true,
		},
		{
			name:        "m. host stripped for matching",
			in:          "https://m.youtube.com/watch?v=ID&si=trk",
			wantOut:     "https://m.youtube.com/watch?v=ID",
			wantChanged: true,
		},
		{
			name:        "music.youtube.com matches",
			in:          "https://music.youtube.com/watch?v=ID&si=trk",
			wantOut:     "https://music.youtube.com/watch?v=ID",
			wantChanged: true,
		},
		{
			name:        "youtube-nocookie matches",
			in:          "https://www.youtube-nocookie.com/embed/ID?si=trk",
			wantOut:     "https://www.youtube-nocookie.com/embed/ID",
			wantChanged: true,
		},
		{
			name:        "fragment preserved",
			in:          "https://youtu.be/ID?si=trk#t=30s",
			wantOut:     "https://youtu.be/ID#t=30s",
			wantChanged: true,
		},
		{
			name:        "non-youtube host with si is untouched (spotify)",
			in:          "https://open.spotify.com/track/abc?si=def123",
			wantOut:     "https://open.spotify.com/track/abc?si=def123",
			wantChanged: false,
		},
		{
			name:        "youtube link without si is untouched",
			in:          "https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=10",
			wantOut:     "https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=10",
			wantChanged: false,
		},
		{
			name:        "si-like prefix but different key is kept",
			in:          "https://www.youtube.com/watch?v=ID&site=foo",
			wantOut:     "https://www.youtube.com/watch?v=ID&site=foo",
			wantChanged: false,
		},
		{
			name:        "empty input",
			in:          "",
			wantOut:     "",
			wantChanged: false,
		},
		{
			name:        "not a url at all",
			in:          "just some text",
			wantOut:     "just some text",
			wantChanged: false,
		},
		{
			name:        "subdomain not in allowlist is untouched",
			in:          "https://gaming.youtube.evil.com/watch?v=ID&si=trk",
			wantOut:     "https://gaming.youtube.evil.com/watch?v=ID&si=trk",
			wantChanged: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := StripShareTracking(tc.in)
			if changed != tc.wantChanged {
				t.Fatalf("changed = %v, want %v (got=%q)", changed, tc.wantChanged, got)
			}
			if got != tc.wantOut {
				t.Errorf("out = %q, want %q", got, tc.wantOut)
			}
		})
	}
}

// TestStripShareTrackingOrderIndependent asserts that the relative
// order of the surviving params does not depend on where si sat.
func TestStripShareTrackingOrderIndependent(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://www.youtube.com/watch?si=X&v=ID&t=5", "https://www.youtube.com/watch?v=ID&t=5"},
		{"https://www.youtube.com/watch?v=ID&t=5&si=X", "https://www.youtube.com/watch?v=ID&t=5"},
		{"https://www.youtube.com/watch?v=ID&si=X&t=5", "https://www.youtube.com/watch?v=ID&t=5"},
	}
	for _, c := range cases {
		got, changed := StripShareTracking(c.in)
		if !changed || got != c.want {
			t.Errorf("StripShareTracking(%q) = %q,%v; want %q,true", c.in, got, changed, c.want)
		}
	}
}

// TestStripShareTrackingTrickyEncodings locks in behavior for inputs
// that previously needed a manual probe:
//   - duplicate si keys: both removed.
//   - a percent-encoded value in a surviving param round-trips
//     (%26 stays %26); a literal-space %20 is re-emitted as the
//     query-equivalent '+' - both decode to a space, so this is a
//     benign canonicalization, not a semantic change.
//   - an uppercase scheme is lowercased by net/url (RFC 3986: schemes
//     are case-insensitive); the host casing is preserved. This is the
//     only normalization stdlib applies and is functionally
//     equivalent - documented, not a bug.
func TestStripShareTrackingTrickyEncodings(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://youtu.be/ID?si=a&si=b", "https://youtu.be/ID"},
		{"https://www.youtube.com/watch?v=a%26b&si=x", "https://www.youtube.com/watch?v=a%26b"},
		{"https://www.youtube.com/watch?v=ID&si=trk&list=PL%20space", "https://www.youtube.com/watch?v=ID&list=PL+space"},
		{"HTTPS://WWW.YOUTUBE.COM/watch?v=ID&si=x", "https://WWW.YOUTUBE.COM/watch?v=ID"},
	}
	for _, c := range cases {
		got, changed := StripShareTracking(c.in)
		if !changed || got != c.want {
			t.Errorf("StripShareTracking(%q) = %q,%v; want %q,true", c.in, got, changed, c.want)
		}
	}
}

func TestSanitizeMessageText(t *testing.T) {
	tests := []struct {
		name        string
		text        string
		entities    []telego.MessageEntity
		wantText    string
		wantChanged bool
	}{
		{
			name:        "bare url in text",
			text:        "look https://youtu.be/ID?si=trk nice",
			wantText:    "look https://youtu.be/ID nice",
			wantChanged: true,
		},
		{
			name:        "trailing period not swallowed",
			text:        "watch https://www.youtube.com/watch?v=ID&si=trk.",
			wantText:    "watch https://www.youtube.com/watch?v=ID.",
			wantChanged: true,
		},
		{
			name:        "scheme-less youtu.be in text",
			text:        "youtu.be/ID?si=trk",
			wantText:    "youtu.be/ID",
			wantChanged: true,
		},
		{
			name: "url entity present (text scan still rewrites the bare url)",
			text: "https://www.youtube.com/watch?v=ID&si=trk",
			entities: []telego.MessageEntity{
				{Type: "url", Offset: 0, Length: 41},
			},
			wantText:    "https://www.youtube.com/watch?v=ID",
			wantChanged: true,
		},
		{
			name: "text_link entity: changed true but text unmodified",
			text: "click here",
			entities: []telego.MessageEntity{
				{Type: "text_link", Offset: 6, Length: 4, URL: "https://youtu.be/ID?si=trk"},
			},
			wantText:    "click here",
			wantChanged: true,
		},
		{
			name: "text_link entity to spotify: not changed",
			text: "click here",
			entities: []telego.MessageEntity{
				{Type: "text_link", Offset: 6, Length: 4, URL: "https://open.spotify.com/track/x?si=y"},
			},
			wantText:    "click here",
			wantChanged: false,
		},
		{
			name:        "multiple links mixed youtube/spotify",
			text:        "yt https://youtu.be/A?si=1 and sp https://open.spotify.com/track/B?si=2 end",
			wantText:    "yt https://youtu.be/A and sp https://open.spotify.com/track/B?si=2 end",
			wantChanged: true,
		},
		{
			name:        "two youtube links both stripped",
			text:        "https://youtu.be/A?si=1 https://www.youtube.com/watch?v=B&si=2",
			wantText:    "https://youtu.be/A https://www.youtube.com/watch?v=B",
			wantChanged: true,
		},
		{
			name:        "no links",
			text:        "just a normal message",
			wantText:    "just a normal message",
			wantChanged: false,
		},
		{
			name:        "youtube link without si untouched",
			text:        "https://www.youtube.com/watch?v=ID",
			wantText:    "https://www.youtube.com/watch?v=ID",
			wantChanged: false,
		},
		{
			name:        "empty text no entities",
			text:        "",
			wantText:    "",
			wantChanged: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := SanitizeMessageText(tc.text, tc.entities)
			if changed != tc.wantChanged {
				t.Fatalf("changed = %v, want %v (text=%q)", changed, tc.wantChanged, got)
			}
			if got != tc.wantText {
				t.Errorf("text = %q, want %q", got, tc.wantText)
			}
		})
	}
}

// recYTSender records every call so middleware tests can assert delete
// + repost behavior. DeleteErr forces the no-rights fallback path.
type recYTSender struct {
	mu sync.Mutex

	DeleteErr error

	Deletes    []*telego.DeleteMessageParams
	Messages   []*telego.SendMessageParams
	Photos     []*telego.SendPhotoParams
	Videos     []*telego.SendVideoParams
	Animations []*telego.SendAnimationParams
	Documents  []*telego.SendDocumentParams
}

func (r *recYTSender) SendMessage(_ context.Context, p *telego.SendMessageParams) (*telego.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Messages = append(r.Messages, p)
	return &telego.Message{MessageID: 1000}, nil
}
func (r *recYTSender) DeleteMessage(_ context.Context, p *telego.DeleteMessageParams) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Deletes = append(r.Deletes, p)
	return r.DeleteErr
}
func (r *recYTSender) SendPhoto(_ context.Context, p *telego.SendPhotoParams) (*telego.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Photos = append(r.Photos, p)
	return &telego.Message{MessageID: 1001}, nil
}
func (r *recYTSender) SendVideo(_ context.Context, p *telego.SendVideoParams) (*telego.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Videos = append(r.Videos, p)
	return &telego.Message{MessageID: 1002}, nil
}
func (r *recYTSender) SendAnimation(_ context.Context, p *telego.SendAnimationParams) (*telego.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Animations = append(r.Animations, p)
	return &telego.Message{MessageID: 1003}, nil
}
func (r *recYTSender) SendDocument(_ context.Context, p *telego.SendDocumentParams) (*telego.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Documents = append(r.Documents, p)
	return &telego.Message{MessageID: 1004}, nil
}

func ytTestMessage(text string) *telego.Message {
	return &telego.Message{
		MessageID: 42,
		Date:      time.Now().Unix(),
		Chat:      telego.Chat{ID: -1001234567890, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 200, Username: "alice", FirstName: "Alice"},
		Text:      text,
	}
}

func TestHandleSanitizeTextDeletesAndReposts(t *testing.T) {
	snd := &recYTSender{}
	msg := ytTestMessage("look https://youtu.be/ID?si=trk")
	newText, _ := SanitizeMessageText(msg.Text, msg.Entities)

	handleSanitize(context.Background(), snd, testLogger(), msg, newText, "")

	if len(snd.Deletes) != 1 {
		t.Fatalf("expected 1 delete, got %d", len(snd.Deletes))
	}
	if snd.Deletes[0].MessageID != 42 {
		t.Errorf("delete targeted message %d, want 42", snd.Deletes[0].MessageID)
	}
	if len(snd.Messages) != 1 {
		t.Fatalf("expected 1 repost message, got %d", len(snd.Messages))
	}
	body := snd.Messages[0].Text
	if !strings.Contains(body, "alice") {
		t.Errorf("repost missing attribution, got %q", body)
	}
	if !strings.Contains(body, "https://youtu.be/ID") || strings.Contains(body, "si=trk") {
		t.Errorf("repost body should carry cleaned link without si, got %q", body)
	}
	if snd.Messages[0].ParseMode != telego.ModeHTML {
		t.Errorf("repost should use HTML parse mode, got %q", snd.Messages[0].ParseMode)
	}
	// Repost is a fresh message, not a reply.
	if snd.Messages[0].ReplyParameters != nil {
		t.Errorf("repost must not be a reply")
	}
}

func TestHandleSanitizePhotoRepostByFileID(t *testing.T) {
	snd := &recYTSender{}
	msg := ytTestMessage("")
	msg.Caption = "see https://youtu.be/ID?si=trk"
	msg.Photo = []telego.PhotoSize{
		{FileID: "small", Width: 90, Height: 90},
		{FileID: "big", Width: 1280, Height: 720},
	}
	newCap, _ := SanitizeMessageText(msg.Caption, msg.CaptionEntities)

	handleSanitize(context.Background(), snd, testLogger(), msg, "", newCap)

	if len(snd.Deletes) != 1 {
		t.Fatalf("expected delete, got %d", len(snd.Deletes))
	}
	if len(snd.Photos) != 1 {
		t.Fatalf("expected 1 SendPhoto, got %d (messages=%d)", len(snd.Photos), len(snd.Messages))
	}
	if snd.Photos[0].Photo.FileID != "big" {
		t.Errorf("should resend largest photo file_id, got %q", snd.Photos[0].Photo.FileID)
	}
	cap := snd.Photos[0].Caption
	if !strings.Contains(cap, "alice") || !strings.Contains(cap, "https://youtu.be/ID") || strings.Contains(cap, "si=trk") {
		t.Errorf("photo caption wrong: %q", cap)
	}
}

func TestHandleSanitizeVideoRepostByFileID(t *testing.T) {
	snd := &recYTSender{}
	msg := ytTestMessage("")
	msg.Caption = "https://www.youtube.com/watch?v=ID&si=trk"
	msg.Video = &telego.Video{FileID: "vid123"}
	nc, _ := SanitizeMessageText(msg.Caption, msg.CaptionEntities)

	handleSanitize(context.Background(), snd, testLogger(), msg, "", nc)

	if len(snd.Videos) != 1 || snd.Videos[0].Video.FileID != "vid123" {
		t.Fatalf("expected SendVideo with file_id vid123, got %+v", snd.Videos)
	}
	if strings.Contains(snd.Videos[0].Caption, "si=trk") {
		t.Errorf("video caption still has si: %q", snd.Videos[0].Caption)
	}
}

// Repost-first contract (critic S1): the cleaned copy is posted BEFORE
// any delete, so a missing Delete right can never destroy content - the
// repost stands and the original is simply kept (a stale si= duplicate
// is the lesser evil vs. data loss).
func TestHandleSanitizeDeleteFailsRepostStandsOriginalKept(t *testing.T) {
	snd := &recYTSender{DeleteErr: errors.New("not enough rights to delete")}
	msg := ytTestMessage("look https://youtu.be/ID?si=trk")
	newText, _ := SanitizeMessageText(msg.Text, msg.Entities)

	handleSanitize(context.Background(), snd, testLogger(), msg, newText, "")

	// Exactly one send: the reposted cleaned copy (no second text
	// fallback - the repost already succeeded).
	if len(snd.Messages) != 1 {
		t.Fatalf("expected the reposted cleaned copy, got %d messages", len(snd.Messages))
	}
	// Delete was attempted after the successful repost and failed; the
	// original is therefore kept (not lost).
	if len(snd.Deletes) != 1 {
		t.Fatalf("expected a delete attempt after repost, got %d", len(snd.Deletes))
	}
	m := snd.Messages[0]
	if !strings.Contains(m.Text, "https://youtu.be/ID") || strings.Contains(m.Text, "si=trk") {
		t.Errorf("repost should carry the cleaned link without si=, got %q", m.Text)
	}
	if !strings.Contains(m.Text, "писал") {
		t.Errorf("repost should carry the attribution header, got %q", m.Text)
	}
}

func TestHandleSanitizeTextLinkEntityUsesReplyFallback(t *testing.T) {
	snd := &recYTSender{}
	msg := ytTestMessage("click here")
	msg.Entities = []telego.MessageEntity{
		{Type: "text_link", Offset: 6, Length: 4, URL: "https://youtu.be/ID?si=trk"},
	}
	newText, changed := SanitizeMessageText(msg.Text, msg.Entities)
	if !changed {
		t.Fatal("text_link to tracked yt link must report changed")
	}

	handleSanitize(context.Background(), snd, testLogger(), msg, newText, "")

	// Body is not visibly changed -> must NOT delete, must reply.
	if len(snd.Deletes) != 0 {
		t.Errorf("text_link case must not delete (cannot faithfully repost), got %d deletes", len(snd.Deletes))
	}
	if len(snd.Messages) != 1 {
		t.Fatalf("expected reply-fallback message, got %d", len(snd.Messages))
	}
	if snd.Messages[0].ReplyParameters == nil {
		t.Errorf("text_link fallback must be a reply")
	}
	if !strings.Contains(snd.Messages[0].Text, "https://youtu.be/ID") {
		t.Errorf("text_link fallback should list the cleaned link, got %q", snd.Messages[0].Text)
	}
}

// TestSanitizeDecisionExclusions covers the pure gate the middleware
// wraps: the same exclusion set as statsCountHandler plus the
// "nothing changed" pass-through. The side-effecting half is covered
// by the handleSanitize tests above. telego exposes no test
// constructor for th.Context and ctx.Next panics on a zero-value
// Context (nil route stack), so the thin Next-wrapping closure itself
// is not exercised in a unit test - documented limitation. The closure
// is a two-line wrapper around sanitizeDecision + handleSanitize, both
// fully covered here.
func TestSanitizeDecisionExclusions(t *testing.T) {
	cases := []struct {
		name    string
		mut     func(m *telego.Message)
		wantAct bool
	}{
		{"tracked link by human acts", func(m *telego.Message) {}, true},
		{"bot author skipped", func(m *telego.Message) { m.From.IsBot = true }, false},
		{"no from skipped", func(m *telego.Message) { m.From = nil }, false},
		{"sender chat skipped", func(m *telego.Message) { m.SenderChat = &telego.Chat{ID: -100} }, false},
		{"anonymous admin skipped", func(m *telego.Message) { m.From.ID = anonAdminID(t) }, false},
		{"no tracked link skipped", func(m *telego.Message) { m.Text = "plain message" }, false},
		{"spotify si link skipped", func(m *telego.Message) {
			m.Text = "https://open.spotify.com/track/x?si=y"
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := ytTestMessage("https://youtu.be/ID?si=trk")
			c.mut(m)
			act, _, _ := sanitizeDecision(m)
			if act != c.wantAct {
				t.Errorf("sanitizeDecision act = %v, want %v", act, c.wantAct)
			}
		})
	}

	t.Run("nil message", func(t *testing.T) {
		if act, _, _ := sanitizeDecision(nil); act {
			t.Error("nil message must not act")
		}
	})
}

// anonAdminID returns the GroupAnonymousBot user id and asserts that
// shared.IsAnonymousAdmin still recognizes it, so the exclusion test
// fails loudly if that constant ever changes.
func anonAdminID(t *testing.T) int64 {
	t.Helper()
	const id = int64(1087968824) // GroupAnonymousBot
	if !shared.IsAnonymousAdmin(id) {
		t.Fatalf("shared.IsAnonymousAdmin(%d) = false; anonymous-admin id changed", id)
	}
	return id
}
