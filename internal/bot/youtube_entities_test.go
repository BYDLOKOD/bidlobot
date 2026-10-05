package bot

import (
	"strings"
	"testing"

	"github.com/mymmrac/telego"
)

// coveredText returns the substring of text covered by the entity, resolved
// through UTF-16 offsets the way Telegram clients do.
func coveredText(text string, e telego.MessageEntity) string {
	var b strings.Builder
	units := 0
	for _, r := range text {
		w := 1
		if r > 0xFFFF {
			w = 2
		}
		if units >= int(e.Offset) && units+w <= int(e.Offset+e.Length) {
			b.WriteRune(r)
		}
		units += w
	}
	return b.String()
}

// Astral characters before a shortened url entity must not shift the
// remapped offsets: the entity still covers exactly the cleaned link.
func TestSanitizeMessageTextEntityOffsetsAstral(t *testing.T) {
	text := "🎉 look https://youtu.be/ID?si=trk end"
	link := "https://youtu.be/ID?si=trk"
	// "🎉 " = 3 units, "look " = 5 units -> link starts at unit 8.
	entities := []telego.MessageEntity{
		{Type: "bold", Offset: 0, Length: 3},
		{Type: "url", Offset: 8, Length: len(link)},
	}
	got, gotEnts, changed, err := SanitizeMessageText(text, entities)
	if err != nil || !changed {
		t.Fatalf("err=%v changed=%v", err, changed)
	}
	want := "🎉 look https://youtu.be/ID end"
	if got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
	for _, e := range gotEnts {
		switch e.Type {
		case "bold":
			if e.Offset != 0 || e.Length != 3 {
				t.Errorf("bold entity shifted: %+v", e)
			}
		case "url":
			if coveredText(got, e) != "https://youtu.be/ID" {
				t.Errorf("url entity covers %q, want the cleaned link", coveredText(got, e))
			}
		}
	}
}

// A hidden text_link URL is rewritten while the anchor text, astral
// characters included, stays byte-identical and its offsets unchanged.
func TestSanitizeMessageTextTextLinkAstralAnchor(t *testing.T) {
	text := "🚀🚀 click"
	entities := []telego.MessageEntity{
		{Type: "text_link", Offset: 5, Length: 5, URL: "https://youtu.be/X?si=y"},
	}
	got, gotEnts, changed, err := SanitizeMessageText(text, entities)
	if err != nil || !changed {
		t.Fatalf("err=%v changed=%v", err, changed)
	}
	if got != text {
		t.Fatalf("anchor text mutated: %q", got)
	}
	if len(gotEnts) != 1 {
		t.Fatalf("expected the entity kept, got %+v", gotEnts)
	}
	e := gotEnts[0]
	if e.Offset != 5 || e.Length != 5 || e.URL != "https://youtu.be/X" {
		t.Fatalf("entity not preserved: %+v", e)
	}
	if coveredText(got, e) != "click" {
		t.Fatalf("entity covers %q, want the untouched anchor", coveredText(got, e))
	}
}

// An entity fully swallowed by a deleted span is dropped; an entity
// partially overlapping one is clipped to the surviving units.
func TestSanitizeMessageTextDropsAndClipsSwallowedEntities(t *testing.T) {
	text := "pre https://youtu.be/ID?si=trk post"
	// "si=trk" occupies units [24,30) and is deleted with the "?" span [23,30).
	fully := []telego.MessageEntity{{Type: "italic", Offset: 24, Length: 6}}
	got, gotEnts, changed, err := SanitizeMessageText(text, fully)
	if err != nil || !changed {
		t.Fatalf("err=%v changed=%v", err, changed)
	}
	if got != "pre https://youtu.be/ID post" {
		t.Fatalf("text = %q", got)
	}
	if len(gotEnts) != 0 {
		t.Fatalf("fully deleted entity must be dropped, got %+v", gotEnts)
	}

	// [22,28) covers "D?si=t": only "D" (1 unit) survives.
	partial := []telego.MessageEntity{{Type: "italic", Offset: 22, Length: 6}}
	got, gotEnts, changed, err = SanitizeMessageText(text, partial)
	if err != nil || !changed {
		t.Fatalf("err=%v changed=%v", err, changed)
	}
	if len(gotEnts) != 1 || gotEnts[0].Offset != 22 || gotEnts[0].Length != 1 {
		t.Fatalf("partially overlapping entity must be clipped to 1 unit, got %+v", gotEnts)
	}
	if coveredText(got, gotEnts[0]) != "D" {
		t.Fatalf("clipped entity covers %q, want \"D\"", coveredText(got, gotEnts[0]))
	}
}

// Malformed input fails closed: out-of-range or surrogate-splitting entity
// offsets and invalid UTF-8 report an error without touching anything.
func TestSanitizeMessageTextFailsClosed(t *testing.T) {
	text := "hello"
	if _, _, changed, err := SanitizeMessageText(text, []telego.MessageEntity{
		{Type: "bold", Offset: 100, Length: 10},
	}); err == nil || changed {
		t.Fatal("out-of-range entity must error without changing anything")
	}
	// "🎉" spans units [0,2); an offset of 1 would split the surrogate pair.
	if _, _, changed, err := SanitizeMessageText("🎉x", []telego.MessageEntity{
		{Type: "bold", Offset: 1, Length: 1},
	}); err == nil || changed {
		t.Fatal("surrogate-splitting offset must error without changing anything")
	}
	if _, _, changed, err := SanitizeMessageText("\xff\xfe", nil); err == nil || changed {
		t.Fatal("invalid UTF-8 must error without changing anything")
	}
}

// Deletion shapes the span editor produces at the query level: percent-
// encoded keys count, a dangling separator survives, and a bare key with
// no value is still a pair.
func TestStripShareTrackingSpanEdges(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://www.youtube.com/watch?v=ID&s%69=x", "https://www.youtube.com/watch?v=ID"},
		{"https://www.youtube.com/watch?v=ID&si=x&", "https://www.youtube.com/watch?v=ID&"},
		{"https://www.youtube.com/watch?si", "https://www.youtube.com/watch"},
		{"https://www.youtube.com/watch?si=&v=ID", "https://www.youtube.com/watch?v=ID"},
		{"https://www.youtube.com/watch?si=x#frag?t=1", "https://www.youtube.com/watch#frag?t=1"},
	}
	for _, c := range cases {
		got, changed := StripShareTracking(c.in)
		if !changed || got != c.want {
			t.Errorf("StripShareTracking(%q) = %q changed=%v, want %q", c.in, got, changed, c.want)
		}
	}
}
