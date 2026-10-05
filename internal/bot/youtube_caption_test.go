package bot

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/mymmrac/telego"
)

// scriptYTImmSender is a copy-capable fake whose SendMessage can be told to
// fail on the Nth call, which the shared recYTSender cannot. IDs are chosen
// so a returned id can never collide with the original message id (42).
type scriptYTImmSender struct {
	mu        sync.Mutex
	copies    []*telego.CopyMessagesParams
	messages  []*telego.SendMessageParams
	deletes   []*telego.DeleteMessageParams
	singles   []*telego.CopyMessageParams
	sent      int
	failOn    int // 1-based SendMessage call index that fails; 0 = never
	failErr   error
	deleteErr error
}

func (s *scriptYTImmSender) SendMessage(_ context.Context, p *telego.SendMessageParams) (*telego.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent++
	if s.failOn == s.sent {
		return nil, s.failErr
	}
	s.messages = append(s.messages, p)
	return &telego.Message{MessageID: 2000 + s.sent}, nil
}

func (s *scriptYTImmSender) CopyMessage(_ context.Context, p *telego.CopyMessageParams) (*telego.MessageID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.singles = append(s.singles, p)
	return &telego.MessageID{MessageID: 2100}, nil
}

func (s *scriptYTImmSender) CopyMessages(_ context.Context, p *telego.CopyMessagesParams) ([]telego.MessageID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.copies = append(s.copies, p)
	return []telego.MessageID{{MessageID: 3000}}, nil
}

func (s *scriptYTImmSender) DeleteMessage(_ context.Context, p *telego.DeleteMessageParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.deletes = append(s.deletes, p)
	return nil
}

func (s *scriptYTImmSender) deleteIDs() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]int, 0, len(s.deletes))
	for _, d := range s.deletes {
		ids = append(ids, d.MessageID)
	}
	return ids
}

// A caption within the message limit is returned as one untouched part.
func TestSplitYouTubeCaptionShort(t *testing.T) {
	text := strings.Repeat("а", 100)
	entities := []telego.MessageEntity{{Type: "bold", Offset: 3, Length: 4}}
	parts, err := splitYouTubeCaption(text, entities)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	if parts[0].text != text || len(parts[0].entities) != 1 {
		t.Fatalf("short caption must pass through: %+v", parts[0])
	}
}

// Splitting retains every byte, keeps every part within the message limit
// and never cuts an astral character in half.
func TestSplitYouTubeCaptionRetainsBytesAndUnits(t *testing.T) {
	text := strings.Repeat("🎉", 2100) + strings.Repeat("а", 100) // 4200 + 100 UTF-16 units
	parts, err := splitYouTubeCaption(text, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parts) < 2 {
		t.Fatalf("expected a split, got %d part(s)", len(parts))
	}
	var rejoined strings.Builder
	total := 0
	for _, p := range parts {
		rejoined.WriteString(p.text)
		units := utf16Length(p.text)
		if units > 4096 {
			t.Fatalf("part has %d UTF-16 units, limit is 4096", units)
		}
		total += units
	}
	if rejoined.String() != text {
		t.Fatalf("split lost or changed bytes: %d vs %d", rejoined.Len(), len(text))
	}
	if total != utf16Length(text) {
		t.Fatalf("unit count changed: %d vs %d", total, utf16Length(text))
	}
}

// An indivisible entity (url) crossing the boundary moves the cut left so
// the whole URL lands in one part with its length intact.
func TestSplitYouTubeCaptionMovesBoundaryOffURL(t *testing.T) {
	const filler = 4090
	const link = "https://youtu.be/ID?si=trk"
	text := strings.Repeat("а", filler) + link
	entities := []telego.MessageEntity{{Type: "url", Offset: filler, Length: len(link)}}
	parts, err := splitYouTubeCaption(text, entities)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(parts))
	}
	if len(parts[0].entities) != 0 {
		t.Fatalf("filler part must carry no entities: %+v", parts[0].entities)
	}
	if len(parts[1].entities) != 1 {
		t.Fatalf("link part must carry the url entity: %+v", parts[1].entities)
	}
	e := parts[1].entities[0]
	if e.Type != "url" || e.Offset != 0 || e.Length != len(link) {
		t.Fatalf("entity not moved whole: %+v", e)
	}
	if parts[1].text != link {
		t.Fatalf("link part text = %q, want %q", parts[1].text, link)
	}
}

// A text_link anchor may be clipped at the boundary; both halves keep the
// URL and rejoin to the original anchor text.
func TestSplitYouTubeCaptionClipsTextLinkAnchor(t *testing.T) {
	const filler = 4093
	const anchor = "watch it"
	text := strings.Repeat("а", filler) + anchor
	entities := []telego.MessageEntity{{Type: "text_link", Offset: filler, Length: len(anchor), URL: "https://youtu.be/ID"}}
	parts, err := splitYouTubeCaption(text, entities)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var cover strings.Builder
	halves := 0
	for _, p := range parts {
		for _, e := range p.entities {
			if e.Type != "text_link" {
				continue
			}
			halves++
			if e.URL != "https://youtu.be/ID" {
				t.Fatalf("clipped half lost the URL: %+v", e)
			}
			units := 0
			for _, r := range p.text {
				w := 1
				if r > 0xFFFF {
					w = 2
				}
				if units >= e.Offset && units+w <= int(e.Offset+e.Length) {
					cover.WriteRune(r)
				}
				units += w
			}
		}
	}
	if halves != 2 {
		t.Fatalf("expected the anchor clipped into 2 halves, got %d", halves)
	}
	if cover.String() != anchor {
		t.Fatalf("halves rejoin to %q, want %q", cover.String(), anchor)
	}
}

// An indivisible entity longer than a whole message rejects the copy
// instead of producing a part that Telegram would refuse.
func TestSplitYouTubeCaptionRejectsOversizedIndivisibleEntity(t *testing.T) {
	text := strings.Repeat("а", 5000)
	entities := []telego.MessageEntity{{Type: "url", Offset: 0, Length: 5000}}
	if _, err := splitYouTubeCaption(text, entities); err == nil {
		t.Fatal("an oversized indivisible entity must error")
	}
}

// Happy path of the long-caption flow: the media is copied caption-less,
// the cleaned caption arrives as one text part replying to the copy, and
// the original is deleted last.
func TestHandleSanitizeLongCaptionMediaPlusPart(t *testing.T) {
	snd := &scriptYTImmSender{}
	msg := ytTestMessage("")
	msg.Photo = []telego.PhotoSize{{FileID: "p", Width: 10, Height: 10}}
	msg.Caption = strings.Repeat("а", 1100) + " https://youtu.be/ID?si=trk"

	_, plan := sanitizeDecision(msg)
	if plan == nil {
		t.Fatal("expected the caption to qualify")
	}
	handleSanitize(context.Background(), snd, testLogger(), msg, plan)

	if len(snd.copies) != 1 {
		t.Fatalf("expected 1 caption-less media copy, got %d", len(snd.copies))
	}
	cp := snd.copies[0]
	if !cp.RemoveCaption {
		t.Error("media copy must remove the original caption")
	}
	if cp.MessageIDs[0] != msg.GetMessageID() || cp.FromChatID.ID != msg.Chat.ID {
		t.Errorf("copy must target the original message in the same chat: %+v", cp)
	}
	if len(snd.messages) != 1 {
		t.Fatalf("expected 1 caption part, got %d", len(snd.messages))
	}
	part := snd.messages[0]
	want := strings.Repeat("а", 1100) + " https://youtu.be/ID"
	if part.Text != want {
		t.Errorf("caption part = %q..., want the cleaned caption", part.Text[:60])
	}
	if part.ReplyParameters == nil || part.ReplyParameters.MessageID != 3000 {
		t.Errorf("caption part must reply to the copied media (3000): %+v", part.ReplyParameters)
	}
	if part.LinkPreviewOptions == nil || !part.LinkPreviewOptions.IsDisabled {
		t.Error("caption part must disable the link preview")
	}
	if ids := snd.deleteIDs(); len(ids) != 1 || ids[0] != msg.GetMessageID() {
		t.Fatalf("original must be deleted exactly once, got %v", ids)
	}
}

// A failure partway through the caption parts must roll back only the
// bot's new messages and keep the original.
func TestHandleSanitizeLongCaptionPartialFailureRollsBack(t *testing.T) {
	snd := &scriptYTImmSender{failOn: 2, failErr: errors.New("send exploded")}
	msg := ytTestMessage("")
	msg.Photo = []telego.PhotoSize{{FileID: "p", Width: 10, Height: 10}}
	msg.Caption = strings.Repeat("а", 5000) + " https://youtu.be/ID?si=trk" // 2 parts

	_, plan := sanitizeDecision(msg)
	handleSanitize(context.Background(), snd, testLogger(), msg, plan)

	ids := snd.deleteIDs()
	// Rollback deletes the media copy (3000) and the first part (2001),
	// never the original (42).
	if len(ids) != 2 || ids[0] != 3000 || ids[1] != 2001 {
		t.Fatalf("rollback must delete the copy and the first part, got %v", ids)
	}
}

// A caption that fits the media limit takes the single server-side copy
// with the cleaned caption instead of the split path.
func TestHandleSanitizeMediaShortCaptionSingleCopy(t *testing.T) {
	snd := &scriptYTImmSender{}
	msg := ytTestMessage("")
	msg.Photo = []telego.PhotoSize{{FileID: "p", Width: 10, Height: 10}}
	msg.Caption = "see https://youtu.be/ID?si=trk"

	_, plan := sanitizeDecision(msg)
	handleSanitize(context.Background(), snd, testLogger(), msg, plan)

	if len(snd.singles) != 1 {
		t.Fatalf("expected exactly one copyMessage, got %d (messages=%d copies=%d)",
			len(snd.singles), len(snd.messages), len(snd.copies))
	}
	cp := snd.singles[0]
	if cp.Caption != "see https://youtu.be/ID" {
		t.Errorf("copy caption = %q", cp.Caption)
	}
	if cp.MessageID != msg.GetMessageID() {
		t.Errorf("copy must target the original message, got %d", cp.MessageID)
	}
	if ids := snd.deleteIDs(); len(ids) != 1 || ids[0] != msg.GetMessageID() {
		t.Fatalf("original must be deleted exactly once, got %v", ids)
	}
}
