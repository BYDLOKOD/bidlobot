package bot

// YouTube `si=` content-preserving URL/entity sanitizer core.
//
// Telegram clients append a `si` share-tracking parameter to YouTube
// links created by the native share sheet. This file removes only those
// query pairs (plus the separator bytes required to keep the query
// well-formed) and nothing else: scheme case, host, path, surviving
// params, their order and escaping, and the fragment all survive
// byte-for-byte.
//
// Two entry points share one deletion-span helper so visible bare URLs
// and the hidden targets of text_link entities are cleaned identically:
//
//   - StripShareTracking works on a single raw URL string.
//   - SanitizeMessageText works on a whole message body plus its
//     entities, remapping UTF-16 entity offsets over the deletions.
//
// All internal work is expressed as offset spans in ORIGINAL
// coordinates; the text is rebuilt once, at the end. URLs are never
// round-tripped through net/url (which would canonicalize them);
// url.Parse is used only to decide host eligibility and
// url.QueryUnescape only to match query keys. youtubeHosts/isYouTubeHost,
// urlScanRe and trailingPunct live in youtube_sanitizer.go and are
// reused here.

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/mymmrac/telego"
)

// offsetSpan is a half-open range [start,end) in some coordinate space:
// a byte range into the original text/URL, or a UTF-16 code-unit range
// for entity offsets. Callers keep the unit straight; the functions
// here operate on whichever space the caller passes.
type offsetSpan struct {
	start, end int
}

// StripShareTracking removes every query pair whose decoded key is
// exactly `si` from a YouTube URL, plus the minimum separator bytes
// needed to keep the surviving query well-formed. Everything else is
// returned byte-for-byte identical; no net/url canonicalization is ever
// applied.
//
// Returns (cleaned, true) when at least one `si` pair was removed,
// otherwise (rawURL, false) with the input returned verbatim. Malformed
// or unparseable URLs, non-YouTube hosts and URLs without `si` are left
// untouched.
func StripShareTracking(rawURL string) (string, bool) {
	spans, ok := siDeletions(rawURL)
	if !ok {
		return rawURL, false
	}
	cleaned := deleteSpans(rawURL, spans)
	if cleaned == "" {
		// Defensive: a non-empty input that strips to empty should not
		// happen (we remove query pairs, never the whole URL), but never
		// emit "".
		return rawURL, false
	}
	return cleaned, true
}

// siDeletions returns the byte spans to delete from rawURL to drop
// every `si` query pair, together with the separators needed to keep the
// result well-formed. ok is false when rawURL is not a YouTube URL or
// carries no `si` pair, in which case the caller returns the input
// verbatim.
//
// Query pairs are separated by '&'. For each maximal run of consecutive
// tracked pairs the trailing separator is removed when a surviving pair
// follows; otherwise the preceding separator is removed; and when every
// query pair is tracked the '?' is removed too. Fragments are never
// touched. Keys are matched after url.QueryUnescape, so an encoded
// `%73i` is recognised as `si`.
//
// Returned spans are ascending and non-overlapping.
func siDeletions(rawURL string) ([]offsetSpan, bool) {
	if rawURL == "" {
		return nil, false
	}

	// Host eligibility only. A scheme-less link parses with an empty
	// Host and the host folded into Path, so prepend a temporary scheme
	// for the parse; all offsets below are computed over the raw input,
	// so the synthetic scheme never reaches the result.
	parseTarget := rawURL
	if !strings.Contains(rawURL, "://") {
		parseTarget = "https://" + rawURL
	}
	u, err := url.Parse(parseTarget)
	if err != nil || u.Host == "" || !isYouTubeHost(u.Host) {
		return nil, false
	}

	qIdx := strings.IndexByte(rawURL, '?')
	fragIdx := strings.IndexByte(rawURL, '#')
	if qIdx < 0 || (fragIdx >= 0 && fragIdx < qIdx) {
		return nil, false // no query, or the '?' is inside the fragment
	}
	qEnd := len(rawURL)
	if fragIdx > qIdx {
		qEnd = fragIdx
	}
	if qIdx+1 >= qEnd {
		return nil, false // empty query
	}

	type qpair struct {
		tracked    bool
		start, end int
	}
	var pairs []qpair
	for pos := qIdx + 1; pos < qEnd; {
		pairEnd := qEnd
		if amp := strings.IndexByte(rawURL[pos:qEnd], '&'); amp >= 0 {
			pairEnd = pos + amp
		}
		raw := rawURL[pos:pairEnd]
		key := raw
		if eq := strings.IndexByte(raw, '='); eq >= 0 {
			key = raw[:eq]
		}
		dec, derr := url.QueryUnescape(key)
		if derr != nil {
			dec = key
		}
		pairs = append(pairs, qpair{tracked: dec == "si", start: pos, end: pairEnd})
		if pairEnd == qEnd {
			break
		}
		pos = pairEnd + 1
	}

	trackedAny := false
	for _, p := range pairs {
		if p.tracked {
			trackedAny = true
			break
		}
	}
	if !trackedAny {
		return nil, false
	}

	var spans []offsetSpan
	n := len(pairs)
	for i := 0; i < n; {
		if !pairs[i].tracked {
			i++
			continue
		}
		j := i
		for j+1 < n && pairs[j+1].tracked {
			j++
		}
		switch {
		case j+1 < n:
			// A surviving pair follows: drop the run and the separator
			// that joins it to that pair.
			spans = append(spans, offsetSpan{pairs[i].start, pairs[j+1].start})
		case i > 0:
			// Run at the end of the query: drop the preceding separator
			// together with the run.
			spans = append(spans, offsetSpan{pairs[i].start - 1, pairs[j].end})
		default:
			// Every query pair is tracked: drop the '?' as well.
			spans = append(spans, offsetSpan{qIdx, qEnd})
		}
		i = j + 1
	}
	return spans, true
}

// deleteSpans rebuilds s with every span removed in a single pass,
// copying only when there is something to delete. Spans MUST be
// ascending and non-overlapping; out-of-range or inverted spans are
// ignored defensively.
func deleteSpans(s string, spans []offsetSpan) string {
	if len(spans) == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	last := 0
	for _, sp := range spans {
		if sp.start < last || sp.end > len(s) || sp.start >= sp.end {
			continue
		}
		b.WriteString(s[last:sp.start])
		last = sp.end
	}
	b.WriteString(s[last:])
	return b.String()
}

// SanitizeMessageText removes tracked `si` query pairs from the YouTube
// URLs visible in text and from the hidden targets of text_link
// entities. It returns the corrected text, corrected entities, whether
// anything changed, and an error when safe remapping is impossible.
//
// Visible anchor text of text_link entities is never treated as a URL
// and never edited; only the entity's URL is cleaned. Explicit `url`
// entities are authoritative: scanner candidates overlapping a url
// entity are dropped and each url entity is cleaned over its own range,
// so its boundaries can never be blended with unrelated text. Overlapping
// url entities are ambiguous and fail the call. Bare URLs not covered by
// a url entity are cleaned directly. All resulting deletion spans are
// merged only to rebuild the visible text; distinct URL candidates are
// never unioned.
//
// Every entity field survives; only Offset/Length (and a text_link URL)
// are adjusted. The input text and entity slice are never mutated; on no
// change they are returned as-is.
//
// Entity ranges are UTF-16 code units. A range that is out of bounds,
// has zero/negative length, or splits a surrogate pair makes the whole
// call fail with an error, and the input is returned unchanged. Invalid
// UTF-8 in the text fails the same way.
func SanitizeMessageText(text string, entities []telego.MessageEntity) (string, []telego.MessageEntity, bool, error) {
	if !utf8.ValidString(text) {
		return text, entities, false, fmt.Errorf("bot: message text is not valid UTF-8")
	}
	u16Len := utf16Length(text)

	// Validate every entity range once, then resolve all requested
	// UTF-16 boundaries (two per entity) to byte indices in a single
	// rune walk over the text.
	boundsReq := make([]int, 0, len(entities)*2)
	for i := range entities {
		e := &entities[i]
		if e.Offset < 0 || e.Length <= 0 || e.Offset+e.Length > u16Len {
			return text, entities, false, fmt.Errorf(
				"bot: entity %d range [%d,%d) out of bounds for %d UTF-16 units",
				i, e.Offset, e.Offset+e.Length, u16Len)
		}
		boundsReq = append(boundsReq, e.Offset, e.Offset+e.Length)
	}
	bounds, err := resolveUTF16ToByte(text, boundsReq)
	if err != nil {
		return text, entities, false, err
	}
	b0 := make([]int, len(entities))
	b1 := make([]int, len(entities))
	for i := range entities {
		b0[i] = bounds[entities[i].Offset]
		b1[i] = bounds[entities[i].Offset+entities[i].Length]
	}

	// Visible spans of text_link anchors: excluded from URL scanning so
	// their on-screen contents are preserved verbatim.
	var anchors []offsetSpan
	for i := range entities {
		if entities[i].Type == "text_link" {
			anchors = append(anchors, offsetSpan{b0[i], b1[i]})
		}
	}

	// Explicit url entities are authoritative. Reject ambiguous overlap
	// between two of them; skip any that would cut through an anchor.
	var urlRanges []offsetSpan
	for i := range entities {
		if entities[i].Type != "url" {
			continue
		}
		if overlapsAny(offsetSpan{b0[i], b1[i]}, anchors) {
			continue
		}
		for _, r := range urlRanges {
			if overlapsAny(offsetSpan{b0[i], b1[i]}, []offsetSpan{r}) {
				return text, entities, false, fmt.Errorf(
					"bot: overlapping url entities at [%d,%d)", b0[i], b1[i])
			}
		}
		urlRanges = append(urlRanges, offsetSpan{b0[i], b1[i]})
	}

	// Deletion spans, in original byte coordinates. Each authoritative
	// url entity is cleaned over its own range; a bare-URL scanner
	// candidate is used only when it overlaps neither a url entity nor a
	// text_link anchor.
	var delBytes []offsetSpan
	for _, r := range urlRanges {
		if sub, ok := siDeletions(text[r.start:r.end]); ok {
			for _, sp := range sub {
				delBytes = append(delBytes, offsetSpan{r.start + sp.start, r.start + sp.end})
			}
		}
	}
	for _, loc := range urlScanRe.FindAllStringIndex(text, -1) {
		start, core := loc[0], loc[1]
		for core > start && strings.IndexByte(trailingPunct, text[core-1]) >= 0 {
			core--
		}
		if core <= start {
			continue
		}
		cand := offsetSpan{start, core}
		if overlapsAny(cand, anchors) || overlapsAny(cand, urlRanges) {
			continue
		}
		if sub, ok := siDeletions(text[start:core]); ok {
			for _, sp := range sub {
				delBytes = append(delBytes, offsetSpan{start + sp.start, start + sp.end})
			}
		}
	}
	delBytes = mergeSpans(delBytes)

	// Hidden-link cleaning: rebuild a text_link URL only when it actually
	// changes, and remember the result so it is built once.
	var hidden map[int]string
	for i := range entities {
		if entities[i].Type != "text_link" || entities[i].URL == "" {
			continue
		}
		if cleaned, c := StripShareTracking(entities[i].URL); c {
			if hidden == nil {
				hidden = make(map[int]string)
			}
			hidden[i] = cleaned
		}
	}

	if len(delBytes) == 0 && hidden == nil {
		return text, entities, false, nil
	}

	newText := deleteSpans(text, delBytes)

	// Convert the (byte) deletion spans to UTF-16 once, then remap the
	// already-known entity offsets directly in UTF-16 space.
	delU16 := make([]offsetSpan, 0, len(delBytes))
	if len(delBytes) > 0 {
		convReq := make([]int, 0, len(delBytes)*2)
		for _, sp := range delBytes {
			convReq = append(convReq, sp.start, sp.end)
		}
		conv := resolveByteToUTF16(text, convReq)
		for _, sp := range delBytes {
			delU16 = append(delU16, offsetSpan{conv[sp.start], conv[sp.end]})
		}
	}

	out := make([]telego.MessageEntity, 0, len(entities))
	for i := range entities {
		ns := remapOffset(entities[i].Offset, delU16)
		ne := remapOffset(entities[i].Offset+entities[i].Length, delU16)
		if ne == ns {
			// The entity's entire contents were deleted; a zero-width
			// entity carries no meaning.
			continue
		}
		e := entities[i] // value copy; the input slice is never mutated
		e.Offset = ns
		e.Length = ne - ns
		if cleaned, ok := hidden[i]; ok {
			e.URL = cleaned
		}
		out = append(out, e)
	}
	return newText, out, true, nil
}

// mergeSpans sorts spans and folds overlapping or touching ranges into
// single spans. It is applied to deletion spans before the text rebuild
// so the same bytes are never removed twice.
func mergeSpans(spans []offsetSpan) []offsetSpan {
	if len(spans) < 2 {
		return spans
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		return spans[i].end < spans[j].end
	})
	out := spans[:1]
	for _, sp := range spans[1:] {
		last := &out[len(out)-1]
		if sp.start <= last.end {
			if sp.end > last.end {
				last.end = sp.end
			}
			continue
		}
		out = append(out, sp)
	}
	return out
}

// remapOffset translates an offset in the original space to its position
// in the text with the given ascending, non-overlapping spans deleted.
// An offset inside a deleted span collapses onto the span's start, so
// both ends of a fully deleted entity land on the same point.
func remapOffset(pos int, spans []offsetSpan) int {
	delta := 0
	for _, sp := range spans {
		if pos <= sp.start {
			break
		}
		if pos >= sp.end {
			delta += sp.end - sp.start
			continue
		}
		delta += pos - sp.start
		break
	}
	return pos - delta
}

// overlapsAny reports whether r shares at least one unit with any span.
// Empty spans cover no units and never overlap.
func overlapsAny(r offsetSpan, spans []offsetSpan) bool {
	if r.start >= r.end {
		return false
	}
	for _, sp := range spans {
		if sp.start >= sp.end {
			continue
		}
		if r.start < sp.end && sp.start < r.end {
			return true
		}
	}
	return false
}

// utf16Length returns the number of UTF-16 code units in s, matching
// Telegram's entity offset/length unit.
func utf16Length(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// resolveUTF16ToByte maps the requested UTF-16 code-unit offsets to byte
// indices with a single rune walk over s. Requests must be non-negative;
// an offset past the end of the text or one that splits a surrogate pair
// returns an error. The returned map has one entry per distinct request.
func resolveUTF16ToByte(s string, requested []int) (map[int]int, error) {
	out := make(map[int]int, len(requested))
	if len(requested) == 0 {
		return out, nil
	}
	req := append([]int(nil), requested...)
	sort.Ints(req)
	uniq := req[:0]
	for i, v := range req {
		if i == 0 || v != req[i-1] {
			uniq = append(uniq, v)
		}
	}
	if uniq[0] < 0 {
		return nil, fmt.Errorf("bot: negative UTF-16 offset %d", uniq[0])
	}

	k := 0
	for k < len(uniq) && uniq[k] == 0 {
		out[0] = 0
		k++
	}
	u := 0
	for i, r := range s {
		w := 1
		if r > 0xFFFF {
			w = 2
		}
		if k < len(uniq) && uniq[k] < u+w {
			// uniq[k] lies strictly inside this rune's code units.
			return nil, fmt.Errorf("bot: UTF-16 offset %d splits a surrogate pair", uniq[k])
		}
		u += w
		end := i + utf8.RuneLen(r)
		for k < len(uniq) && uniq[k] == u {
			out[u] = end
			k++
		}
	}
	if k < len(uniq) {
		return nil, fmt.Errorf("bot: UTF-16 offset %d out of range", uniq[k])
	}
	return out, nil
}

// resolveByteToUTF16 maps the requested byte indices to UTF-16 code-unit
// offsets with a single rune walk over s. Every request MUST fall on a
// rune boundary (all callers derive them from ASCII separators or entity
// boundaries). The returned map has one entry per distinct request.
func resolveByteToUTF16(s string, requested []int) map[int]int {
	out := make(map[int]int, len(requested))
	if len(requested) == 0 {
		return out
	}
	req := append([]int(nil), requested...)
	sort.Ints(req)
	uniq := req[:0]
	for i, v := range req {
		if i == 0 || v != req[i-1] {
			uniq = append(uniq, v)
		}
	}
	k := 0
	for k < len(uniq) && uniq[k] == 0 {
		out[0] = 0
		k++
	}
	u := 0
	for i, r := range s {
		if r > 0xFFFF {
			u += 2
		} else {
			u++
		}
		end := i + utf8.RuneLen(r)
		for k < len(uniq) && uniq[k] == end {
			out[end] = u
			k++
		}
	}
	return out
}
