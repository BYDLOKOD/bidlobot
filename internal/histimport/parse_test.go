package histimport_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/veschin/bidlobot/internal/histimport"
)

// realExport is the single shared fixture for every test in this package.
// It is the legacy cmd/bidlobot-import schema (the contract of the user's
// 41k-message production export) extended for the in-process importer:
//
//   - every message carries "id":N (the monthly idempotency key);
//   - text appears in BOTH real shapes: a plain string (id 2,4) and an
//     array of {type,text} segments whose concatenation is the body
//     (id 3,7);
//   - text_entities arrays exercise all four tracked nomination types
//     (custom_emoji, code, mention, bot_command) - all four on the Олег
//     December message (id 7), one (code) on the Олег August message
//     (id 3), an untracked "plain" on id 2;
//   - the linked-channel auto-post (from=null, from_id=channel999, id 5);
//   - the anonymous-admin post (from_id=chat777, id 6);
//   - two service events: invite_members with actor_id=user100 (id 1,
//     teaches user100's July join) and pin_message with no actor (id 8).
//
// Every date_unixtime is the exact UTC unix value of its date string
// (verified), so MessageTime is unambiguous and the derived month is
// stable. All expected aggregates below are hand-computed from this text.
//
// Hand-computed parse facts:
//
//	type=message rows : ids 2,3,4,5,6,7                   -> TotalMessages 6
//	type=service rows : ids 1,8                           -> ServiceMsgs   2
//	id 5 from=null                                        -> SkippedNilFrom 1
//	id 6 from_id=chat777 (not user*)                      -> SkippedNonUser 1
//	no row lacks a timestamp                              -> SkippedNoTS    0
//	accepted users                                        -> user100, user200
//	MaxMessageID = 8 (id update is type-agnostic, incl. service id 8)
//	MinMessageID = 1 (lowest id > 0, the service invite)
//
//	user100 "Олег"          : msgs id 2,3,7 -> Count 3
//	  MinTS 2025-08-05, MaxTS 2025-12-01 (December)
//	  JoinedAt 2025-07-01 (July, from service invite id 1)
//	user200 "Старик Молчун" : msg  id 4     -> Count 1
//	  MaxTS 2025-08-10 (August); JoinedAt zero (no service row for it)
//
// Per-message monthly Sample (text shape -> flattened body -> runes):
//
//	id 2 "Благодарю"                              2025-08 runes 9  kw 0
//	id 3 "люблю "+"cursor" = "люблю cursor"       2025-08 runes 12 kw 1 Code 1
//	id 4 "последнее что я писал"                  2025-08 runes 21 kw 0
//	id 7 "http://x"+" свежак курсор Cursor CURSOR"
//	      = "http://x свежак курсор Cursor CURSOR" 2025-12 runes 36 kw 3
//	      custom_emoji 1, code 1, mention 1, bot_command 1
const realExport = `{
  "name": "тестовая",
  "type": "public_supergroup",
  "id": 3920475340,
  "messages": [
    {"id":1,"type":"service","date":"2025-07-01T00:00:00","date_unixtime":"1751328000","action":"invite_members","actor":"Олег","actor_id":"user100","members":["Старик Молчун"]},
    {"id":2,"type":"message","date":"2025-08-05T00:02:00","date_unixtime":"1754352120","from":"Олег","from_id":"user100","text":"Благодарю","text_entities":[{"type":"plain","text":"Благодарю"}]},
    {"id":3,"type":"message","date":"2025-08-20T12:00:00","date_unixtime":"1755691200","from":"Олег","from_id":"user100","text":[{"type":"plain","text":"люблю "},{"type":"code","text":"cursor"}],"text_entities":[{"type":"code","text":"cursor"}]},
    {"id":4,"type":"message","date":"2025-08-10T00:00:00","date_unixtime":"1754784000","from":"Старик Молчун","from_id":"user200","text":"последнее что я писал"},
    {"id":5,"type":"message","date":"2025-09-01T00:00:00","date_unixtime":"1756684800","from":null,"from_id":"channel999","text":"linked channel autopost"},
    {"id":6,"type":"message","date":"2025-09-02T00:00:00","date_unixtime":"1756771200","from":"Аноним Админ","from_id":"chat777","text":"anon admin post"},
    {"id":7,"type":"message","date":"2025-12-01T10:00:00","date_unixtime":"1764583200","from":"Олег","from_id":"user100","text":[{"type":"link","text":"http://x"},{"type":"plain","text":" свежак курсор Cursor CURSOR"}],"text_entities":[{"type":"custom_emoji","text":"🙂"},{"type":"code","text":"x"},{"type":"mention","text":"@a"},{"type":"bot_command","text":"/s"}]},
    {"id":8,"type":"service","date":"2025-09-03T00:00:00","date_unixtime":"1756857600","action":"pin_message"}
  ]
}`

func TestParseUserID(t *testing.T) {
	cases := []struct {
		in     string
		wantID int64
		wantOK bool
	}{
		{"user1786612758", 1786612758, true},
		{"user1", 1, true},
		{"channel1786612758", 0, false}, // linked-channel auto-post
		{"chat1786612758", 0, false},    // anonymous admin
		{"user0", 0, false},             // id must be positive
		{"user-5", 0, false},
		{"user", 0, false},
		{"userabc", 0, false},
		{"1786612758", 0, false}, // bare digits, no prefix
		{"", 0, false},
	}
	for _, c := range cases {
		id, ok := histimport.ParseUserID(c.in)
		if id != c.wantID || ok != c.wantOK {
			t.Errorf("ParseUserID(%q) = (%d,%v), want (%d,%v)", c.in, id, ok, c.wantID, c.wantOK)
		}
	}
}

func TestMessageTime(t *testing.T) {
	// date_unixtime wins when present and valid.
	got, ok := histimport.MessageTime(histimport.RawMessage{
		DateUnixtime: "1754352120", Date: "2025-08-05T00:02:00",
	})
	if !ok || got.Unix() != 1754352120 {
		t.Fatalf("unixtime path: got %v ok=%v", got, ok)
	}
	if got.Location() != time.UTC {
		t.Fatalf("unixtime path must be UTC, got %v", got.Location())
	}
	// Fallback to the date string when unixtime is missing.
	got, ok = histimport.MessageTime(histimport.RawMessage{Date: "2025-08-05T00:02:00"})
	if !ok || got.Year() != 2025 || got.Month() != time.August {
		t.Fatalf("date fallback: got %v ok=%v", got, ok)
	}
	// Garbage unixtime falls through to the date string.
	got, ok = histimport.MessageTime(histimport.RawMessage{
		DateUnixtime: "not-a-number", Date: "2025-08-05T00:02:00",
	})
	if !ok || got.Year() != 2025 {
		t.Fatalf("garbage unixtime should fall back to date: got %v ok=%v", got, ok)
	}
	// A non-positive unixtime is rejected and falls back to the date.
	got, ok = histimport.MessageTime(histimport.RawMessage{
		DateUnixtime: "0", Date: "2025-08-05T00:02:00",
	})
	if !ok || got.Year() != 2025 {
		t.Fatalf("zero unixtime should fall back to date: got %v ok=%v", got, ok)
	}
	// Both empty -> no timestamp.
	if _, ok := histimport.MessageTime(histimport.RawMessage{}); ok {
		t.Fatal("empty message must not yield a timestamp")
	}
}

func TestParseRealSchema(t *testing.T) {
	st, err := histimport.Parse(context.Background(), strings.NewReader(realExport), nil, false)
	if err != nil {
		t.Fatal(err)
	}

	if st.ChatName != "тестовая" || st.ChatType != "public_supergroup" {
		t.Fatalf("chat meta: name=%q type=%q", st.ChatName, st.ChatType)
	}
	if st.TotalMessages != 6 { // ids 2,3,4,5,6,7 are type=message
		t.Fatalf("TotalMessages = %d, want 6", st.TotalMessages)
	}
	if st.ServiceMsgs != 2 { // ids 1,8
		t.Fatalf("ServiceMsgs = %d, want 2", st.ServiceMsgs)
	}
	if st.SkippedNilFrom != 1 { // id 5 (channel autopost, from=null)
		t.Fatalf("SkippedNilFrom = %d, want 1", st.SkippedNilFrom)
	}
	if st.SkippedNonUser != 1 { // id 6 (chat777, from set but not user*)
		t.Fatalf("SkippedNonUser = %d, want 1", st.SkippedNonUser)
	}
	if st.SkippedNoTS != 0 {
		t.Fatalf("SkippedNoTS = %d, want 0", st.SkippedNoTS)
	}
	if len(st.Users) != 2 {
		t.Fatalf("unique users = %d, want 2", len(st.Users))
	}

	// MaxMessageID is updated for EVERY decoded element regardless of type
	// (parse.go updates it before the type switch), so the service event
	// id 8 is the high-water mark, not the last message id 7.
	if st.MaxMessageID != 8 {
		t.Fatalf("MaxMessageID = %d, want 8 (highest id, the service event)", st.MaxMessageID)
	}
	if st.MinMessageID != 1 {
		t.Fatalf("MinMessageID = %d, want 1 (lowest id, the service invite)", st.MinMessageID)
	}

	oleg := st.Users[100]
	if oleg == nil || oleg.Count != 3 {
		t.Fatalf("user100 Count = %v, want 3 (ids 2,3,7)", oleg)
	}
	if oleg.FirstName != "Олег" {
		t.Fatalf("user100 FirstName = %q, want Олег", oleg.FirstName)
	}
	// Last message is the December one (id 7), not an August one.
	if oleg.MaxTS.Month() != time.December || oleg.MaxTS.Year() != 2025 {
		t.Fatalf("user100 MaxTS = %v, want December 2025", oleg.MaxTS)
	}
	if oleg.MinTS.Month() != time.August {
		t.Fatalf("user100 MinTS month = %v, want August", oleg.MinTS.Month())
	}
	// Join learned from the service invite (id 1, 2025-07-01).
	if oleg.JoinedAt.IsZero() || oleg.JoinedAt.Month() != time.July {
		t.Fatalf("user100 JoinedAt = %v, want July from service invite", oleg.JoinedAt)
	}

	silent := st.Users[200]
	if silent == nil || silent.Count != 1 {
		t.Fatalf("user200 Count = %v, want 1", silent)
	}
	if silent.FirstName != "Старик Молчун" {
		t.Fatalf("user200 FirstName = %q, want Старик Молчун", silent.FirstName)
	}
	if silent.MaxTS.Month() != time.August {
		t.Fatalf("user200 last wrote Aug, got %v", silent.MaxTS.Month())
	}
	// No service event mentions user200, so JoinedAt stays zero here;
	// Ingest is what falls it back to MinTS.
	if !silent.JoinedAt.IsZero() {
		t.Fatalf("user200 JoinedAt = %v, want zero (no service row)", silent.JoinedAt)
	}

	// Earliest is the July service invite (touchRange runs for the
	// invite's actor), Latest is the December message.
	if st.Earliest.Month() != time.July {
		t.Fatalf("Earliest = %v, want July", st.Earliest)
	}
	if st.Latest.Month() != time.December {
		t.Fatalf("Latest = %v, want December", st.Latest)
	}
}

// recordingSink captures every MessageEvent so the test can assert the
// single streaming pass fans out exactly the accepted user messages with
// the correct id and monthly Sample, proving import and live converge.
type recordingSink struct {
	events []histimport.MessageEvent
}

func (s *recordingSink) OnMessage(ev histimport.MessageEvent) error {
	s.events = append(s.events, ev)
	return nil
}

func TestParseSinkFanout(t *testing.T) {
	sink := &recordingSink{}
	st, err := histimport.Parse(context.Background(), strings.NewReader(realExport), sink, false)
	if err != nil {
		t.Fatal(err)
	}
	if st.TotalMessages != 6 {
		t.Fatalf("sanity: TotalMessages = %d, want 6", st.TotalMessages)
	}

	// Exactly the four accepted user messages reach the sink, in stream
	// order: id 2,3,7 (Олег) and id 4 (Старик). The skipped rows (nil
	// from id 5, non-user id 6, service id 1,8) must NOT appear.
	if len(sink.events) != 4 {
		t.Fatalf("sink received %d events, want 4 (ids 2,3,4,7)", len(sink.events))
	}

	byID := map[int64]histimport.MessageEvent{}
	for _, e := range sink.events {
		byID[e.MessageID] = e
	}
	for _, id := range []int64{2, 3, 4, 7} {
		if _, ok := byID[id]; !ok {
			t.Fatalf("expected a sink event for message id %d", id)
		}
	}

	// id 2: "Благодарю" plain string, 9 runes, untracked "plain" entity,
	// no keyword.
	e2 := byID[2]
	if e2.Sample.UserID != 100 || e2.Sample.Month != "2025-08" {
		t.Errorf("id2 Sample user/month = %d/%q, want 100/2025-08", e2.Sample.UserID, e2.Sample.Month)
	}
	if e2.Sample.Runes != 9 {
		t.Errorf("id2 Runes = %d, want 9", e2.Sample.Runes)
	}
	if e2.Sample.CustomEmoji+e2.Sample.Code+e2.Sample.Mention+e2.Sample.BotCommand != 0 {
		t.Errorf("id2 must have no tracked entities, got %+v", e2.Sample)
	}
	if e2.Sample.Keyword != 0 {
		t.Errorf("id2 Keyword = %d, want 0", e2.Sample.Keyword)
	}
	if e2.Sample.TS.Format("2006-01") != "2025-08" {
		t.Errorf("id2 Sample.TS month = %q, want 2025-08", e2.Sample.TS.Format("2006-01"))
	}

	// id 3: array text flattened to "люблю cursor" = 12 runes; one code
	// entity; "cursor" -> keyword 1.
	e3 := byID[3]
	if e3.Sample.Runes != 12 {
		t.Errorf("id3 Runes = %d, want 12 (flattened array body)", e3.Sample.Runes)
	}
	if e3.Sample.Code != 1 {
		t.Errorf("id3 Code = %d, want 1", e3.Sample.Code)
	}
	if e3.Sample.Keyword != 1 {
		t.Errorf("id3 Keyword = %d, want 1 (cursor)", e3.Sample.Keyword)
	}
	if e3.Sample.Month != "2025-08" {
		t.Errorf("id3 Month = %q, want 2025-08", e3.Sample.Month)
	}

	// id 7: array body "http://x свежак курсор Cursor CURSOR" = 36 runes;
	// all four entity types once each; курсор/Cursor/CURSOR -> keyword 3.
	e7 := byID[7]
	if e7.Sample.Runes != 36 {
		t.Errorf("id7 Runes = %d, want 36", e7.Sample.Runes)
	}
	if e7.Sample.CustomEmoji != 1 || e7.Sample.Code != 1 ||
		e7.Sample.Mention != 1 || e7.Sample.BotCommand != 1 {
		t.Errorf("id7 entity tally wrong: %+v", e7.Sample)
	}
	if e7.Sample.Keyword != 3 {
		t.Errorf("id7 Keyword = %d, want 3", e7.Sample.Keyword)
	}
	if e7.Sample.Month != "2025-12" {
		t.Errorf("id7 Month = %q, want 2025-12", e7.Sample.Month)
	}

	// id 4: "последнее что я писал" plain string = 21 runes, no entities,
	// no keyword, user200.
	e4 := byID[4]
	if e4.Sample.UserID != 200 {
		t.Errorf("id4 Sample.UserID = %d, want 200", e4.Sample.UserID)
	}
	if e4.Sample.Runes != 21 {
		t.Errorf("id4 Runes = %d, want 21", e4.Sample.Runes)
	}
	if e4.Sample.Keyword != 0 {
		t.Errorf("id4 Keyword = %d, want 0", e4.Sample.Keyword)
	}
}

func TestParseRejectsEmptyExport(t *testing.T) {
	_, err := histimport.Parse(context.Background(),
		strings.NewReader(`{"name":"x","messages":[]}`), nil, false)
	if err == nil {
		t.Fatal("an export with no user messages must be rejected with a helpful error")
	}
	if !strings.Contains(err.Error(), "Export chat history") {
		t.Fatalf("error should hint at the right export menu, got: %v", err)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := histimport.Parse(context.Background(),
		strings.NewReader(`not json at all`), nil, false); err == nil {
		t.Fatal("non-JSON input must error")
	}
}

func TestParseContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before Parse starts

	// A multi-message stream: the messages loop checks ctx between
	// elements, so an already-cancelled context aborts with ctx.Err()
	// rather than completing the parse.
	_, err := histimport.Parse(ctx, strings.NewReader(realExport), nil, false)
	if err == nil {
		t.Fatal("Parse must abort on a cancelled context")
	}
	if err != context.Canceled {
		t.Fatalf("Parse error = %v, want context.Canceled", err)
	}
}
