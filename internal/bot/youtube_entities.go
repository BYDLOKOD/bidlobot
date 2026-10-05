package bot

// YouTube `si=` sanitizer: drops only `si` query pairs (plus the minimal
// separators), preserving every other byte. No net/url canonicalization;
// spans stay in ORIGINAL coordinates and the text is rebuilt once.

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/mymmrac/telego"
)

// offsetSpan is a half-open [start,end) range in a caller-chosen space:
// bytes into the original text/URL, or UTF-16 units for entity offsets.
type offsetSpan struct {
	start, end int
}

// StripShareTracking removes every `si` query pair from a YouTube URL
// plus the minimal separator bytes. Returns (cleaned, true) on removal,
// else (rawURL, false); malformed, non-YouTube and si-less URLs pass through.
func StripShareTracking(rawURL string) (string, bool) {
	spans, ok := siDeletions(rawURL)
	if !ok {
		return rawURL, false
	}
	cleaned := deleteSpans(rawURL, spans)
	if cleaned == "" {
		// Defensive: never emit an empty result for non-empty input.
		return rawURL, false
	}
	return cleaned, true
}

// siDeletions returns ascending, non-overlapping byte spans that remove
// every `si` pair (key matched after url.QueryUnescape, so `%73i` counts)
// plus the joining separator of each run ('&', or '?' when every pair is
// tracked); ok is false for non-YouTube, no-query or no-`si` input.
func siDeletions(rawURL string) ([]offsetSpan, bool) {
	if rawURL == "" {
		return nil, false
	}

	// Host eligibility only; the synthetic scheme never shifts offsets.
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

	// One scan. pendingRunStart marks the current run of tracked pairs;
	// lastPairEnd is the last pair actually scanned, so a trailing run
	// keeps a dangling '&' (which is not a pair) instead of eating it.
	var spans []offsetSpan
	pendingRunStart, lastPairEnd := -1, qEnd
	sawSurvivor := false
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
		lastPairEnd = pairEnd
		if dec == "si" {
			if pendingRunStart < 0 {
				pendingRunStart = pos
			}
		} else {
			sawSurvivor = true
			if pendingRunStart >= 0 {
				spans = append(spans, offsetSpan{pendingRunStart, pos})
				pendingRunStart = -1
			}
		}
		if pairEnd == qEnd {
			break
		}
		pos = pairEnd + 1
	}
	if pendingRunStart >= 0 {
		if sawSurvivor {
			// Trailing run: drop it plus the separator before it, but
			// keep a dangling '&' at the very end of the query.
			spans = append(spans, offsetSpan{pendingRunStart - 1, lastPairEnd})
		} else {
			// Every pair tracked: drop the '?' too.
			spans = append(spans, offsetSpan{qIdx, qEnd})
		}
	}
	if len(spans) == 0 {
		return nil, false
	}
	return spans, true
}

// deleteSpans rebuilds s without the spans, which MUST be ascending and
// non-overlapping; invalid spans are skipped defensively.
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

// SanitizeMessageText cleans visible YouTube URLs and text_link targets,
// returning text, entities, changed and an error. Entity ranges are
// UTF-16; text_link anchor text is never scanned and url entities are
// authoritative. Inputs are never mutated and bad ranges/UTF-8 fail closed.
func SanitizeMessageText(text string, entities []telego.MessageEntity) (string, []telego.MessageEntity, bool, error) {
	if !utf8.ValidString(text) {
		return text, entities, false, fmt.Errorf("bot: message text is not valid UTF-8")
	}
	u16Len := utf16Length(text)

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

	// text_link anchor spans are never scanned as URLs.
	var anchors []offsetSpan
	for i := range entities {
		if entities[i].Type == "text_link" {
			anchors = append(anchors, offsetSpan{b0[i], b1[i]})
		}
	}

	// url entities are authoritative: reject mutual overlap and skip any
	// that cuts through an anchor.
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

	// Deletion spans in original byte coordinates.
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

	// Clean each text_link URL, remembering only the changed ones.
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

	// Map the merged byte spans to UTF-16 in one walk over disjoint slices.
	delU16 := make([]offsetSpan, 0, len(delBytes))
	prevByte, u16 := 0, 0
	for _, sp := range delBytes {
		u16 += utf16Length(text[prevByte:sp.start])
		start := u16
		u16 += utf16Length(text[sp.start:sp.end])
		delU16 = append(delU16, offsetSpan{start, u16})
		prevByte = sp.end
	}

	out := make([]telego.MessageEntity, 0, len(entities))
	for i := range entities {
		ns := remapOffset(entities[i].Offset, delU16)
		ne := remapOffset(entities[i].Offset+entities[i].Length, delU16)
		if ne == ns {
			// Fully deleted; a zero-width entity carries no meaning.
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
// single spans.
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

// remapOffset shifts an original-space offset past the deleted spans; an
// offset inside a span collapses onto its start.
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

// overlapsAny reports whether r shares a unit with any span; empty spans
// never overlap.
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

// utf16Length counts UTF-16 code units, Telegram's entity unit.
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

// resolveUTF16ToByte maps requested UTF-16 offsets to byte indices in one
// rune walk; negative, past-end or surrogate-splitting offsets error.
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
