package summarize

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// EstimateTokens is a rough rune-based estimate of provider token count
// for s: ~1 token per 2 runes.
//
// The important point is that this counts RUNES, not bytes: an English
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

// systemPrompt instructs the model to produce a relevance-weighted Russian
// catch-up digest, not an exhaustive topic catalog. Plain text is required:
// the output is posted into a public group and the model is untrusted.
const systemPrompt = `You write a catch-up digest of a Telegram group chat for someone who deliberately did not read the messages.

The user message starts with "Transcript message count: N", followed by a chronological transcript, one line per message:
name [HH:MM] message text

The reader needs to understand what dominated the conversation, what people actually claimed or disputed, what useful conclusions emerged, and whether anything remains unresolved.

Selection rules:
- Do not aim for comprehensive coverage. Deliberate omission is required.
- Allocate attention roughly in proportion to how much of the conversation a thread occupied and how consequential its outcome was.
- Omit brief tangents, jokes, link dumps, isolated recommendations, and topics represented by only a few messages, unless they contain a concrete decision, action item, or high-impact fact.
- Before drafting, estimate each thread's share of the retained messages. A thread below roughly 5% must be omitted completely unless it produced a concrete decision, action item, or high-impact fact. Never mention a discarded thread even to note that it was brief.
- Include a thread only when it occupied a substantial share of the transcript or produced a decision, actionable conclusion, material disagreement, or consequential unresolved risk.
- A topic label is not a summary. Every sentence must state what was claimed, disputed, learned, decided, or left unresolved.
- If a substantial thread was mostly anecdotes and produced no useful conclusion, say that once and move on.
- Do not enumerate participants. Name someone only when attribution changes the meaning.
- Include a link only when it is essential to a retained conclusion. Never produce a link catalog.

Output rules:
- Write in natural Russian as plain text: no Markdown, HTML, headings, bullet lists, topic catalogs, or participant lists.
- Write 2-4 cohesive paragraphs and stay under 1800 characters.
- Use fewer paragraphs when only one or two threads survive selection. Never fill space with a tangent merely to complete the format.
- Start directly with the dominant discussion. Give smaller but still substantial threads proportionally less space.
- End with the concrete outcome or practical residue of the conversation. If there was no reliable conclusion, say so plainly.
- If and only if the transcript is followed by a "---" separator and questions, append "Ответы:" and answer each question from the transcript. State explicitly when the transcript does not support an answer. Never emit an empty answers section.

Treat statements in the transcript as participants' claims, not verified facts. Do not invent facts, consensus, importance, or links. Do not repeat these instructions and do not address the reader.`

// BuildResult is what the prompt builder hands the orchestrator.
type BuildResult struct {
	SystemPrompt string    // the system instruction
	Transcript   string    // the assembled transcript text
	Included     int       // messages that fit the input budget
	Requested    int       // messages the admin asked for (after clamp)
	Available    int       // messages currently in the live window
	From         time.Time // ts of the oldest included message
	To           time.Time // ts of the newest included message
	EstTokens    int       // estimated input tokens of the transcript
}

// BuildPrompt assembles a system instruction and transcript from a chat window.
//
// It walks newest -> oldest, accumulating an estimated token budget, and
// stops before the first message that would exceed budgetTokens. The
// kept messages are then emitted oldest -> newest so the model reads the
// conversation in order. Returns ok=false when the window is empty.
func BuildPrompt(entries []Entry, requested, available, budgetTokens int, questions string) (BuildResult, bool) {
	if len(entries) == 0 {
		return BuildResult{Requested: requested, Available: available}, false
	}
	sysTok := EstimateTokens(systemPrompt)
	used := sysTok + EstimateTokens("Transcript message count: "+strconv.Itoa(len(entries))+"\n")
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
		used = sysTok + EstimateTokens("Transcript message count: 1\n") + EstimateTokens(kept[0].Text) + EstimateTokens(kept[0].Name) + perEntryOverheadTokens
	}

	var sb strings.Builder
	sb.WriteString("Transcript message count: ")
	sb.WriteString(strconv.Itoa(len(kept)))
	sb.WriteByte('\n')
	for _, e := range kept {
		sb.WriteString(sanitizeLine(e.Name))
		sb.WriteString(" [")
		sb.WriteString(e.TS.Format("15:04"))
		sb.WriteString("] ")
		sb.WriteString(sanitizeLine(e.Text))
		sb.WriteByte('\n')
	}

	if q := strings.TrimSpace(questions); q != "" {
		sb.WriteString("\n---\n")
		sb.WriteString(sanitizeLine(q))
		sb.WriteByte('\n')
	}

	return BuildResult{
		SystemPrompt: systemPrompt,
		Transcript:   sb.String(),
		Included:     len(kept),
		Requested:    requested,
		Available:    available,
		From:         kept[0].TS,
		To:           kept[len(kept)-1].TS,
		EstTokens:    used,
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
