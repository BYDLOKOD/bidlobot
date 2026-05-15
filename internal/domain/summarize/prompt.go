package summarize

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/veschin/bidlobot/internal/shared/glm"
)

// EstimateTokens is a rough rune-based estimate of the GLM token count
// for s: ~1 token per 2 runes.
//
// GLM's tokenizer has no maintained Go port. The load-bearing point is
// that this counts RUNES, not bytes: an English byte-length chars/4
// heuristic would wildly mis-budget Russian (Cyrillic is 2 bytes/rune
// and tokenizes denser than English). rune/2 is in the ballpark for
// Russian prose but is NOT a guaranteed upper bound - code blocks, long
// URLs and snake_case identifiers (common in this IT chat) can exceed
// 0.5 token/rune, so a code-dense window can still under-count. That is
// accepted: the input budget keeps a deliberate margin and the
// provider's own context-length 400 is the hard backstop, mapped to a
// "lower N" message (after one paid round trip - hence the margin).
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (utf8.RuneCountInString(s) + 1) / 2
}

// perEntryOverheadTokens covers the "name [15:04] " prefix and the
// newline we add per transcript line, independent of body length.
const perEntryOverheadTokens = 8

// systemPrompt instructs the model in English (internal artifact) to
// emit a Russian, plain-text, bounded, structured summary. Plain text is
// required: the output is posted into a public group and the model is
// untrusted - Markdown/HTML entities from it must not be interpreted.
const systemPrompt = `You summarize a Telegram group chat for the admins of a Russian-speaking IT community.

The user message is a chronological transcript, one line per message, formatted:
name [HH:MM] message text

Write the summary IN RUSSIAN, as PLAIN TEXT only - no Markdown, no HTML, no asterisks, no backticks, no links markup. Keep it under 2800 characters total. Structure it as these sections, each on its own lines, omitting any section that has no content:

Кратко: 2-3 sentences, the gist.
Темы: the main discussion threads, one per line, prefixed with "- ".
Решения: concrete decisions or conclusions, "- " prefixed; omit if none.
Ссылки: notable links/resources mentioned, "- " prefixed; omit if none.
Вопросы: unresolved questions left open, "- " prefixed; omit if none.

Summarize only what is actually in the transcript. Do not invent participants, facts, or links. Do not repeat these instructions. Do not address the reader.`

// BuildResult is what the prompt builder hands the orchestrator.
type BuildResult struct {
	Messages  []glm.Message
	Included  int       // messages that fit the input budget
	Requested int       // messages the admin asked for (after clamp)
	Available int       // messages currently in the live window
	From      time.Time // ts of the oldest included message
	To        time.Time // ts of the newest included message
	EstTokens int       // estimated input tokens of the transcript
}

// BuildPrompt assembles the GLM messages from a chat window.
//
// It walks newest -> oldest, accumulating an estimated token budget, and
// stops before the first message that would exceed budgetTokens. The
// kept messages are then emitted oldest -> newest so the model reads the
// conversation in order. Returns ok=false when the window is empty.
func BuildPrompt(entries []Entry, requested, available, budgetTokens int) (BuildResult, bool) {
	if len(entries) == 0 {
		return BuildResult{Requested: requested, Available: available}, false
	}
	sysTok := EstimateTokens(systemPrompt)
	used := sysTok
	// Index of the oldest entry we keep; default to "all of them".
	start := 0
	for i := len(entries) - 1; i >= 0; i-- {
		cost := EstimateTokens(entries[i].Text) + EstimateTokens(entries[i].Name) + perEntryOverheadTokens
		if used+cost > budgetTokens && i != len(entries)-1 {
			start = i + 1
			break
		}
		used += cost
	}
	kept := entries[start:]
	if len(kept) == 0 {
		// Even a single newest message overflowed the budget: keep just
		// it and let the provider's own limit be the final arbiter.
		kept = entries[len(entries)-1:]
		start = len(entries) - 1
		used = sysTok + EstimateTokens(kept[0].Text) + EstimateTokens(kept[0].Name) + perEntryOverheadTokens
	}

	var sb strings.Builder
	for _, e := range kept {
		// No leading '@': a literal @handle here would be echoed by the
		// model and rendered as a real, notifying Telegram mention in
		// the public result. Names are plain; the output is additionally
		// mention-defused downstream as defense in depth.
		sb.WriteString(sanitizeLine(e.Name))
		sb.WriteString(" [")
		sb.WriteString(e.TS.Format("15:04"))
		sb.WriteString("] ")
		sb.WriteString(sanitizeLine(e.Text))
		sb.WriteByte('\n')
	}

	return BuildResult{
		Messages: []glm.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: sb.String()},
		},
		Included:  len(kept),
		Requested: requested,
		Available: available,
		From:      kept[0].TS,
		To:        kept[len(kept)-1].TS,
		EstTokens: used,
	}, true
}

// sanitizeLine collapses newlines/tabs/CR into spaces so one message
// stays one transcript line (the format the system prompt promises the
// model). Leading/trailing space is trimmed; interior runs are kept as
// single spaces to preserve word boundaries cheaply.
func sanitizeLine(s string) string {
	if s == "" {
		return ""
	}
	repl := strings.NewReplacer("\n", " ", "\r", " ", "\t", " ")
	s = repl.Replace(s)
	return strings.Join(strings.Fields(s), " ")
}
