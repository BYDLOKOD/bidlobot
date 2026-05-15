// Package histimport is the in-process Telegram Desktop chat-export
// ingest. It was extracted verbatim (logic-preserving) from the
// cmd/bidlobot-import CLI so the exact same streaming parser + membership
// rollup can run both from the CLI (bot stopped) and inside the running
// bot from a DM upload (no bbolt flock, since the bot's own *bolt.DB
// handle is reused). The single streaming pass also feeds the monthly
// statistics engine via a MessageSink, so one read of a multi-hundred-MB
// export populates both membership and monthstats.
package histimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/veschin/bidlobot/internal/domain/monthstats"
)

// RawMessage is the per-element decode target. It is the legacy
// cmd/bidlobot-import schema plus `id` (the export message id, the
// idempotency key for the additive monthly counters) and `text` /
// `text_entities` (needed for the monthly char/entity/keyword tallies).
// `text` is string OR []{type,text} in real exports; flattenText handles
// both.
type RawMessage struct {
	ID           int64           `json:"id"`
	Type         string          `json:"type"`
	Date         string          `json:"date"`
	DateUnixtime string          `json:"date_unixtime"`
	From         *string         `json:"from"`
	FromID       string          `json:"from_id"`
	Action       string          `json:"action"`
	ActorID      string          `json:"actor_id"`
	Text         json.RawMessage `json:"text"`
	TextEntities []EntityRef     `json:"text_entities"`
}

// EntityRef is one element of text_entities. Its Type uses the same
// vocabulary as the Bot API MessageEntity.type, so monthstats counts
// identically whether the data arrived live or via import.
type EntityRef struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Aggregate is the per-user membership rollup (was main.aggregate).
type Aggregate struct {
	UserID    int64
	FirstName string
	Count     int64
	MinTS     time.Time
	MaxTS     time.Time
	JoinedAt  time.Time
}

// Stats is the parse result (was main.stats). Field semantics are
// unchanged from the CLI; MaxMessageID / MinMessageID are added for the
// monthly import watermark.
type Stats struct {
	TotalMessages  int64
	ServiceMsgs    int64
	SkippedNilFrom int64
	SkippedNonUser int64
	SkippedNoTS    int64
	Users          map[int64]*Aggregate
	Earliest       time.Time
	Latest         time.Time
	MaxMessageID   int64
	MinMessageID   int64
	ChatName       string
	ChatType       string
}

// MessageEvent is emitted for every accepted user message (one that
// becomes a membership message). Sample carries the monthly counters
// already computed via the shared monthstats counting contract;
// Sample.AbsChatID is left 0 here (Parse does not know the chat) and is
// bound by the sink/Ingest.
type MessageEvent struct {
	MessageID int64
	Sample    monthstats.Sample
}

// MessageSink receives every accepted user message during the single
// streaming pass. Returning an error aborts the parse (fail-closed). A
// nil sink means membership-only (the CLI without --monthly).
type MessageSink interface {
	OnMessage(ev MessageEvent) error
}

func (st *Stats) userFor(uid int64, name string) *Aggregate {
	a, ok := st.Users[uid]
	if !ok {
		a = &Aggregate{UserID: uid}
		st.Users[uid] = a
	}
	if name != "" {
		a.FirstName = name
	}
	return a
}

func (st *Stats) touchRange(ts time.Time) {
	if st.Earliest.IsZero() || ts.Before(st.Earliest) {
		st.Earliest = ts
	}
	if ts.After(st.Latest) {
		st.Latest = ts
	}
}

// Parse streams r once. ctx cancellation is checked between elements so a
// 67k-message parse aborts within one element on shutdown / Stop. sink
// may be nil (membership-only). It opens no DB and no network - pure.
func Parse(ctx context.Context, r io.Reader, sink MessageSink, verbose bool) (*Stats, error) {
	dec := json.NewDecoder(r)
	st := &Stats{Users: make(map[int64]*Aggregate)}

	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("read export: not JSON or empty: %w", err)
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("read key: %w", err)
		}
		key, _ := keyTok.(string)
		switch key {
		case "name":
			_ = dec.Decode(&st.ChatName)
		case "type":
			_ = dec.Decode(&st.ChatType)
		case "messages":
			if err := streamMessages(ctx, dec, st, sink, verbose); err != nil {
				return nil, err
			}
		default:
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, fmt.Errorf("skip key %q: %w", key, err)
			}
		}
	}

	if len(st.Users) == 0 {
		return nil, errors.New("no user messages found - is this a single-chat 'Export chat history' JSON? A public group's account-wide 'Export Telegram Data' only contains your own messages and is not usable here")
	}
	return st, nil
}

func streamMessages(ctx context.Context, dec *json.Decoder, st *Stats, sink MessageSink, verbose bool) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("messages: expected array: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return fmt.Errorf("messages: expected '[', got %v", tok)
	}

	for dec.More() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		var m RawMessage
		if err := dec.Decode(&m); err != nil {
			return fmt.Errorf("decode message: %w", err)
		}
		if m.ID > st.MaxMessageID {
			st.MaxMessageID = m.ID
		}
		if m.ID > 0 && (st.MinMessageID == 0 || m.ID < st.MinMessageID) {
			st.MinMessageID = m.ID
		}

		ts, ok := MessageTime(m)
		if m.Type == "service" {
			st.ServiceMsgs++
			if ok && m.ActorID != "" {
				if uid, isUser := ParseUserID(m.ActorID); isUser {
					a := st.userFor(uid, "")
					if a.JoinedAt.IsZero() || ts.Before(a.JoinedAt) {
						a.JoinedAt = ts
					}
					st.touchRange(ts)
				}
			}
			continue
		}
		if m.Type != "message" {
			continue
		}
		st.TotalMessages++

		if m.From == nil {
			st.SkippedNilFrom++
			continue
		}
		uid, isUser := ParseUserID(m.FromID)
		if !isUser {
			st.SkippedNonUser++
			continue
		}
		if !ok {
			st.SkippedNoTS++
			continue
		}

		a := st.userFor(uid, strings.TrimSpace(*m.From))
		a.Count++
		if a.MinTS.IsZero() || ts.Before(a.MinTS) {
			a.MinTS = ts
		}
		if ts.After(a.MaxTS) {
			a.MaxTS = ts
		}
		st.touchRange(ts)

		if sink != nil {
			if err := sink.OnMessage(MessageEvent{
				MessageID: m.ID,
				Sample:    sampleFromExport(uid, ts, m),
			}); err != nil {
				return fmt.Errorf("monthly sink: %w", err)
			}
		}
	}

	if _, err := dec.Token(); err != nil {
		return fmt.Errorf("messages: unterminated array: %w", err)
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "parsed messages=%d service=%d skip_nil_from=%d skip_non_user=%d skip_no_ts=%d\n",
			st.TotalMessages, st.ServiceMsgs, st.SkippedNilFrom, st.SkippedNonUser, st.SkippedNoTS)
	}
	return nil
}

// sampleFromExport builds the monthly Sample from an export row using the
// SAME monthstats primitives the live path uses, so live and import
// counts converge. AbsChatID is left 0; the sink binds it.
func sampleFromExport(uid int64, ts time.Time, m RawMessage) monthstats.Sample {
	body := flattenText(m.Text)
	s := monthstats.Sample{
		UserID: uid,
		TS:     ts.UTC(),
		Month:  ts.UTC().Format("2006-01"),
		Runes:  monthstats.RuneLen(body),
	}
	for _, e := range m.TextEntities {
		s.AddEntityType(e.Type)
	}
	s.Keyword = int64(monthstats.CountKeyword(body))
	s.Excerpt, s.ExcerptFull = monthstats.Excerpt(body)
	return s
}

// flattenText handles the two real-export shapes of `text`: a plain
// string, or an array of {type,text} segments (whose concatenation is the
// full message text).
func flattenText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	case '[':
		var segs []EntityRef
		if json.Unmarshal(raw, &segs) == nil {
			var b strings.Builder
			for _, sg := range segs {
				b.WriteString(sg.Text)
			}
			return b.String()
		}
	}
	return ""
}

// MessageTime prefers date_unixtime (UTC, unambiguous), falling back to
// the local-wall-clock `date` string treated as UTC.
func MessageTime(m RawMessage) (time.Time, bool) {
	if m.DateUnixtime != "" {
		if sec, err := strconv.ParseInt(m.DateUnixtime, 10, 64); err == nil && sec > 0 {
			return time.Unix(sec, 0).UTC(), true
		}
	}
	if m.Date != "" {
		if t, err := time.Parse("2006-01-02T15:04:05", m.Date); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// ParseUserID extracts the numeric id from "user<digits>". channel* /
// chat* / garbage return isUser=false so anonymous admins and linked-
// channel auto-posts are excluded from membership.
func ParseUserID(fromID string) (int64, bool) {
	const p = "user"
	if !strings.HasPrefix(fromID, p) {
		return 0, false
	}
	id, err := strconv.ParseInt(fromID[len(p):], 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
