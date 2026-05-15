// Package summarize keeps a bounded, RAM-only view of recent chat text
// and turns a window of it into an LLM prompt. Nothing here is ever
// persisted: the Telegram Bot API cannot replay history (no
// getChatHistory; consumed updates are discarded within 24h - verified
// against core.telegram.org/bots/api), so "summarize the last N" can
// only ever mean "the last N this process has heard since it started",
// and that is exactly what this buffer holds. A restart empties it by
// design; raw member text never touches disk or the backup artifact.
package summarize

import (
	"sync"
	"time"
)

// Entry is one human message reduced to what a summary needs.
type Entry struct {
	MsgID  int
	UserID int64
	Name   string // display name at record time (no @handle dependency)
	TS     time.Time
	Text   string // message text or media caption; never empty when stored
}

// BufferConfig bounds memory. Zero values fall back to the defaults
// below; the bot is multi-chat and long-lived, so every dimension is
// capped to keep the resident set predictable.
type BufferConfig struct {
	MaxPerChat   int // entries retained per chat (ring)
	MaxBytesChat int // approx text bytes retained per chat
	MaxChats     int // distinct chats tracked before the LRU evicts one
}

const (
	defaultMaxPerChat   = 2000
	defaultMaxBytesChat = 4 << 20 // 4 MiB of text per chat
	defaultMaxChats     = 256
)

type ring struct {
	entries    []Entry // chronological, oldest at index 0
	bytes      int
	lastRecord time.Time // for cross-chat LRU eviction
}

// Buffer is a concurrency-safe per-chat ring of recent messages.
type Buffer struct {
	mu           sync.Mutex
	chats        map[int64]*ring
	maxPerChat   int
	maxBytesChat int
	maxChats     int
}

// NewBuffer builds a Buffer, applying defaults for any zero field.
func NewBuffer(cfg BufferConfig) *Buffer {
	b := &Buffer{
		chats:        make(map[int64]*ring),
		maxPerChat:   cfg.MaxPerChat,
		maxBytesChat: cfg.MaxBytesChat,
		maxChats:     cfg.MaxChats,
	}
	if b.maxPerChat <= 0 {
		b.maxPerChat = defaultMaxPerChat
	}
	if b.maxBytesChat <= 0 {
		b.maxBytesChat = defaultMaxBytesChat
	}
	if b.maxChats <= 0 {
		b.maxChats = defaultMaxChats
	}
	return b
}

// Record appends e to absChatID's ring, evicting oldest entries until
// both the count and byte caps hold. A single message longer than the
// whole byte cap is still stored (truncation is the prompt builder's
// job, not the buffer's) but it then sits alone.
func (b *Buffer) Record(absChatID int64, e Entry) {
	if e.Text == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	r := b.chats[absChatID]
	if r == nil {
		b.evictChatsLocked()
		r = &ring{}
		b.chats[absChatID] = r
	}
	r.entries = append(r.entries, e)
	r.bytes += len(e.Text)
	r.lastRecord = time.Now()

	for len(r.entries) > b.maxPerChat || (r.bytes > b.maxBytesChat && len(r.entries) > 1) {
		r.bytes -= len(r.entries[0].Text)
		if r.bytes < 0 {
			r.bytes = 0
		}
		r.entries = r.entries[1:]
	}
	// Reclaim the backing array periodically so a long-lived high-churn
	// chat does not pin an ever-growing slice header offset.
	if len(r.entries) > 0 && cap(r.entries) > 4*b.maxPerChat {
		compact := make([]Entry, len(r.entries))
		copy(compact, r.entries)
		r.entries = compact
	}
}

// Update rewrites the text of a previously recorded message (a Telegram
// edit) so the summary reflects the final wording. No-op if the message
// has already been evicted. Byte accounting is kept consistent.
func (b *Buffer) Update(absChatID int64, msgID int, newText string) {
	if newText == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.chats[absChatID]
	if r == nil {
		return
	}
	for i := range r.entries {
		if r.entries[i].MsgID == msgID {
			r.bytes += len(newText) - len(r.entries[i].Text)
			if r.bytes < 0 {
				r.bytes = 0
			}
			r.entries[i].Text = newText
			return
		}
	}
}

// Window returns up to n most-recent entries in chronological order as a
// copy (callers must not see the live backing array). The second result
// is the total currently retained for this chat, so the caller can tell
// the admin "asked for N, only M are in the live window".
func (b *Buffer) Window(absChatID int64, n int) ([]Entry, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.chats[absChatID]
	if r == nil || len(r.entries) == 0 {
		return nil, 0
	}
	total := len(r.entries)
	if n <= 0 || n > total {
		n = total
	}
	out := make([]Entry, n)
	copy(out, r.entries[total-n:])
	return out, total
}

// evictChatsLocked drops the least-recently-recorded chat when the
// distinct-chat cap is already reached and a new chat is about to be
// added. Caller holds b.mu.
func (b *Buffer) evictChatsLocked() {
	if len(b.chats) < b.maxChats {
		return
	}
	var oldestID int64
	var oldestAt time.Time
	first := true
	for id, r := range b.chats {
		if first || r.lastRecord.Before(oldestAt) {
			oldestID, oldestAt, first = id, r.lastRecord, false
		}
	}
	if !first {
		delete(b.chats, oldestID)
	}
}
