package histimport_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veschin/bidlobot/internal/domain/cleanup"
	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/histimport"
	"github.com/veschin/bidlobot/internal/storage"
)

// overlapExport is realExport plus two strictly-higher-id messages in a
// brand-new month (2026-01). Re-ingesting it over a DB already seeded
// with realExport must dedup every base id (<= the prior watermark 8)
// and add ONLY the two new rows - proving the message-id watermark
// excludes the overlap exactly.
//
// Hand-computed new-month facts (2026-01):
//
//	id 20 user100 "новый месяц cursor" runes 18 kw 1
//	id 21 user200 "ещё"                runes  3 kw 0
//	META 2026-01: TotalMsgs 2, TotalRunes 21, Longest 18 user100
const overlapExport = `{
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
    {"id":8,"type":"service","date":"2025-09-03T00:00:00","date_unixtime":"1756857600","action":"pin_message"},
    {"id":20,"type":"message","date":"2026-01-15T08:00:00","date_unixtime":"1768464000","from":"Олег","from_id":"user100","text":"новый месяц cursor"},
    {"id":21,"type":"message","date":"2026-01-20T09:00:00","date_unixtime":"1768899600","from":"Старик Молчун","from_id":"user200","text":"ещё"}
  ]
}`

func newStores(t *testing.T) (*storage.MembershipRepo, *storage.MonthStatsRepo) {
	t.Helper()
	dir := t.TempDir()
	bs, err := storage.NewBoltStore(filepath.Join(dir, "bidlobot.db"))
	if err != nil {
		t.Fatalf("open bolt store: %v", err)
	}
	t.Cleanup(func() { _ = bs.Close() })
	db := bs.DB()
	return storage.NewMembershipRepo(db), storage.NewMonthStatsRepo(db)
}

// userStat finds the per-user MonthUserStat for uid in (absChat, month),
// failing the test if it is absent.
func userStat(t *testing.T, mon *storage.MonthStatsRepo, absChat int64, month string, uid int64) monthstatsUserStat {
	t.Helper()
	_, users, err := mon.GetMonth(context.Background(), absChat, month)
	if err != nil {
		t.Fatalf("GetMonth(%s): %v", month, err)
	}
	for _, u := range users {
		if u.UserID == uid {
			return monthstatsUserStat{
				MsgCount:     u.MsgCount,
				RuneCount:    u.RuneCount,
				CustomEmoji:  u.CustomEmoji,
				Code:         u.Code,
				Mention:      u.Mention,
				BotCommand:   u.BotCommand,
				KeywordCount: u.KeywordCount,
			}
		}
	}
	t.Fatalf("no MonthUserStat for user %d in %s", uid, month)
	return monthstatsUserStat{}
}

type monthstatsUserStat struct {
	MsgCount     int64
	RuneCount    int64
	CustomEmoji  int64
	Code         int64
	Mention      int64
	BotCommand   int64
	KeywordCount int64
}

// absChat is the absolute form of the signed supergroup id
// -1009000002; the importer and storage key on the absolute value.
const absChat int64 = 1009000002

func TestIngestEndToEndContract(t *testing.T) {
	mem, mon := newStores(t)
	ctx := context.Background()

	res, err := histimport.Ingest(ctx, strings.NewReader(realExport), absChat, mem, mon, nil, false)
	if err != nil {
		t.Fatalf("first ingest: %v", err)
	}

	// --- Membership ---------------------------------------------------
	// user100 sent 3 accepted messages in this fixture (ids 2,3,7). (The
	// legacy fixture had 2; this extended fixture adds the December id-7
	// row, so the hand-computed expectation is 3.)
	oleg, err := mem.GetMember(ctx, 100, absChat)
	if err != nil {
		t.Fatalf("get user100: %v", err)
	}
	if oleg.MessageCount != 3 {
		t.Fatalf("user100 MessageCount = %d, want 3 (ids 2,3,7)", oleg.MessageCount)
	}
	if oleg.KnownVia != membership.SourceImport {
		t.Fatalf("user100 KnownVia = %q, want %q", oleg.KnownVia, membership.SourceImport)
	}
	if oleg.JoinedAt.Month() != time.July {
		t.Fatalf("user100 JoinedAt = %v, want July (from the service invite)", oleg.JoinedAt)
	}
	silent, err := mem.GetMember(ctx, 200, absChat)
	if err != nil {
		t.Fatalf("get user200: %v", err)
	}
	if silent.MessageCount != 1 {
		t.Fatalf("user200 MessageCount = %d, want 1", silent.MessageCount)
	}
	// user200 has no service row, so Ingest falls JoinedAt back to its
	// earliest message (id 4, 2025-08-10).
	if silent.JoinedAt.Month() != time.August {
		t.Fatalf("user200 JoinedAt = %v, want August (MinTS fallback)", silent.JoinedAt)
	}

	chat, err := mem.GetChat(ctx, absChat)
	if err != nil {
		t.Fatalf("get chat: %v", err)
	}
	if chat.InstalledAt.Month() != time.July {
		t.Fatalf("chat InstalledAt = %v, want July (earliest event)", chat.InstalledAt)
	}
	if chat.Title != "тестовая" || chat.Type != "public_supergroup" {
		t.Fatalf("chat meta: title=%q type=%q", chat.Title, chat.Type)
	}

	// --- Monthly: 2025-08 (id 2,3 Олег; id 4 Старик) ------------------
	o8 := userStat(t, mon, absChat, "2025-08", 100)
	// id2 runes 9 + id3 runes 12 = 21; id3 has one code entity; id3
	// "cursor" -> 1 keyword.
	if o8.MsgCount != 2 || o8.RuneCount != 21 {
		t.Fatalf("user100 2025-08 = %+v, want MsgCount 2 RuneCount 21", o8)
	}
	if o8.Code != 1 || o8.CustomEmoji != 0 || o8.Mention != 0 || o8.BotCommand != 0 {
		t.Fatalf("user100 2025-08 entities = %+v, want only Code 1", o8)
	}
	if o8.KeywordCount != 1 {
		t.Fatalf("user100 2025-08 KeywordCount = %d, want 1", o8.KeywordCount)
	}
	s8 := userStat(t, mon, absChat, "2025-08", 200)
	if s8.MsgCount != 1 || s8.RuneCount != 21 || s8.KeywordCount != 0 {
		t.Fatalf("user200 2025-08 = %+v, want MsgCount 1 RuneCount 21 kw 0", s8)
	}
	meta8, _, err := mon.GetMonth(ctx, absChat, "2025-08")
	if err != nil {
		t.Fatalf("GetMonth 2025-08: %v", err)
	}
	if meta8 == nil {
		t.Fatal("2025-08 MonthMeta missing")
	}
	// Month total = 2 (Олег) + 1 (Старик) = 3 msgs, 21+21 = 42 runes.
	if meta8.TotalMsgs != 3 || meta8.TotalRunes != 42 {
		t.Fatalf("2025-08 meta = msgs %d runes %d, want 3 / 42", meta8.TotalMsgs, meta8.TotalRunes)
	}
	// Longest August message is id 4 (21 runes, user200), beating id3
	// (12) and id2 (9).
	if meta8.LongestRunes != 21 || meta8.LongestUserID != 200 {
		t.Fatalf("2025-08 longest = %d runes by %d, want 21 by 200",
			meta8.LongestRunes, meta8.LongestUserID)
	}
	if meta8.LongestExcerpt != "последнее что я писал" || !meta8.LongestFull {
		t.Fatalf("2025-08 longest excerpt = %q full=%v",
			meta8.LongestExcerpt, meta8.LongestFull)
	}

	// --- Monthly: 2025-12 (id 7 Олег, all four entity types) ----------
	o12 := userStat(t, mon, absChat, "2025-12", 100)
	if o12.MsgCount != 1 || o12.RuneCount != 36 {
		t.Fatalf("user100 2025-12 = %+v, want MsgCount 1 RuneCount 36", o12)
	}
	if o12.CustomEmoji != 1 || o12.Code != 1 || o12.Mention != 1 || o12.BotCommand != 1 {
		t.Fatalf("user100 2025-12 entities = %+v, want all four = 1", o12)
	}
	if o12.KeywordCount != 3 {
		t.Fatalf("user100 2025-12 KeywordCount = %d, want 3 (курсор/Cursor/CURSOR)", o12.KeywordCount)
	}
	meta12, _, _ := mon.GetMonth(ctx, absChat, "2025-12")
	if meta12 == nil || meta12.TotalMsgs != 1 || meta12.TotalRunes != 36 {
		t.Fatalf("2025-12 meta = %+v, want msgs 1 runes 36", meta12)
	}
	if meta12.LongestRunes != 36 || meta12.LongestUserID != 100 ||
		meta12.LongestExcerpt != "http://x свежак курсор Cursor CURSOR" {
		t.Fatalf("2025-12 longest = %d by %d %q",
			meta12.LongestRunes, meta12.LongestUserID, meta12.LongestExcerpt)
	}

	// --- Result watermark / counters ----------------------------------
	// Accepted = the 4 user messages (ids 2,3,4,7). Watermark advances
	// from 0 to MaxMessageID 8 (the service event id, since the id
	// high-water mark is type-agnostic).
	if res.MonthlyAccepted != 4 {
		t.Fatalf("MonthlyAccepted = %d, want 4", res.MonthlyAccepted)
	}
	if res.MonthlyDeduped != 0 || res.MonthlySkippedLive != 0 {
		t.Fatalf("first run dedup/skip = %d/%d, want 0/0",
			res.MonthlyDeduped, res.MonthlySkippedLive)
	}
	if res.PriorWatermark != 0 || res.NewWatermark != 8 {
		t.Fatalf("watermark %d->%d, want 0->8", res.PriorWatermark, res.NewWatermark)
	}
	if res.MembersWritten != 2 {
		t.Fatalf("MembersWritten = %d, want 2", res.MembersWritten)
	}
	if months, _ := mon.ListMonths(ctx, absChat); len(months) != 2 {
		t.Fatalf("ListMonths = %v, want exactly [2025-08 2025-12]", months)
	}

	// --- Idempotency: re-ingest the identical export ------------------
	res2, err := histimport.Ingest(ctx, strings.NewReader(realExport), absChat, mem, mon, nil, false)
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	// Every message id (max accepted id 7) is <= prior watermark 8, so
	// all four accepted rows dedup and none is re-counted.
	if res2.MonthlyAccepted != 0 || res2.MonthlyDeduped != 4 {
		t.Fatalf("re-import accepted/deduped = %d/%d, want 0/4",
			res2.MonthlyAccepted, res2.MonthlyDeduped)
	}
	oleg2, _ := mem.GetMember(ctx, 100, absChat)
	if oleg2.MessageCount != 3 {
		t.Fatalf("re-import changed user100 count: %d, want still 3", oleg2.MessageCount)
	}
	silent2, _ := mem.GetMember(ctx, 200, absChat)
	if silent2.MessageCount != 1 {
		t.Fatalf("re-import changed user200 count: %d, want still 1", silent2.MessageCount)
	}
	// Monthly counters must be byte-identical to the first run.
	if got := userStat(t, mon, absChat, "2025-08", 100); got != o8 {
		t.Fatalf("re-import mutated user100 2025-08: %+v, want %+v", got, o8)
	}
	if got := userStat(t, mon, absChat, "2025-12", 100); got != o12 {
		t.Fatalf("re-import mutated user100 2025-12: %+v, want %+v", got, o12)
	}
	meta8b, _, _ := mon.GetMonth(ctx, absChat, "2025-08")
	if meta8b.TotalMsgs != 3 || meta8b.TotalRunes != 42 {
		t.Fatalf("re-import mutated 2025-08 meta: msgs %d runes %d, want 3/42",
			meta8b.TotalMsgs, meta8b.TotalRunes)
	}

	// --- Cleanup contract over the real domain ------------------------
	// now is mid-December; threshold 90d -> cutoff 2025-09-16. Олег last
	// wrote 2025-12-01 (safe). Старик last wrote 2025-08-10 (> 90d
	// stale -> the sole candidate).
	now := time.Date(2025, 12, 15, 0, 0, 0, 0, time.UTC)
	prev, err := cleanup.NewService(mem, nil, nil).
		PreviewInactive(ctx, absChat, 90*24*time.Hour, now)
	if err != nil {
		t.Fatalf("cleanup preview: %v", err)
	}
	if prev.KnownMembers != 2 {
		t.Fatalf("KnownMembers = %d, want 2", prev.KnownMembers)
	}
	if len(prev.Candidates) != 1 {
		t.Fatalf("candidates = %d, want 1 (only the silent user)", len(prev.Candidates))
	}
	if prev.Candidates[0].UserID != 200 {
		t.Fatalf("candidate = user %d, want 200 (Старик Молчун)", prev.Candidates[0].UserID)
	}
	if prev.ObservationWindow == 0 {
		t.Fatal("ObservationWindow must be non-zero after import sets InstalledAt")
	}
}

func TestIngestOverlapAddsOnlyNewMonth(t *testing.T) {
	mem, mon := newStores(t)
	ctx := context.Background()

	if _, err := histimport.Ingest(ctx, strings.NewReader(realExport), absChat, mem, mon, nil, false); err != nil {
		t.Fatalf("base ingest: %v", err)
	}
	// Snapshot the pre-overlap state of the old months.
	o8before := userStat(t, mon, absChat, "2025-08", 100)
	o12before := userStat(t, mon, absChat, "2025-12", 100)

	res, err := histimport.Ingest(ctx, strings.NewReader(overlapExport), absChat, mem, mon, nil, false)
	if err != nil {
		t.Fatalf("overlap ingest: %v", err)
	}

	// The overlap export repeats ids 1-8 (all <= prior watermark 8 ->
	// the 4 accepted rows dedup) and adds ids 20,21 (> 8 -> accepted).
	if res.MonthlyAccepted != 2 {
		t.Fatalf("overlap MonthlyAccepted = %d, want 2 (ids 20,21)", res.MonthlyAccepted)
	}
	if res.MonthlyDeduped != 4 {
		t.Fatalf("overlap MonthlyDeduped = %d, want 4 (ids 2,3,4,7)", res.MonthlyDeduped)
	}
	if res.PriorWatermark != 8 || res.NewWatermark != 21 {
		t.Fatalf("overlap watermark %d->%d, want 8->21",
			res.PriorWatermark, res.NewWatermark)
	}

	// Old months must be byte-for-byte unchanged (no double count).
	if got := userStat(t, mon, absChat, "2025-08", 100); got != o8before {
		t.Fatalf("overlap mutated user100 2025-08: %+v, want %+v", got, o8before)
	}
	if got := userStat(t, mon, absChat, "2025-12", 100); got != o12before {
		t.Fatalf("overlap mutated user100 2025-12: %+v, want %+v", got, o12before)
	}

	// New month 2026-01: id20 user100 (18 runes, kw 1), id21 user200
	// (3 runes).
	n100 := userStat(t, mon, absChat, "2026-01", 100)
	if n100.MsgCount != 1 || n100.RuneCount != 18 || n100.KeywordCount != 1 {
		t.Fatalf("user100 2026-01 = %+v, want MsgCount 1 RuneCount 18 kw 1", n100)
	}
	n200 := userStat(t, mon, absChat, "2026-01", 200)
	if n200.MsgCount != 1 || n200.RuneCount != 3 {
		t.Fatalf("user200 2026-01 = %+v, want MsgCount 1 RuneCount 3", n200)
	}
	meta01, _, _ := mon.GetMonth(ctx, absChat, "2026-01")
	if meta01 == nil || meta01.TotalMsgs != 2 || meta01.TotalRunes != 21 {
		t.Fatalf("2026-01 meta = %+v, want msgs 2 runes 21", meta01)
	}
	if meta01.LongestRunes != 18 || meta01.LongestUserID != 100 {
		t.Fatalf("2026-01 longest = %d by %d, want 18 by 100",
			meta01.LongestRunes, meta01.LongestUserID)
	}

	// Membership absolute counts grow to the new totals via max():
	// user100 = ids 2,3,7,20 = 4; user200 = ids 4,21 = 2.
	oleg, _ := mem.GetMember(ctx, 100, absChat)
	if oleg.MessageCount != 4 {
		t.Fatalf("user100 MessageCount after overlap = %d, want 4", oleg.MessageCount)
	}
	silent, _ := mem.GetMember(ctx, 200, absChat)
	if silent.MessageCount != 2 {
		t.Fatalf("user200 MessageCount after overlap = %d, want 2", silent.MessageCount)
	}
	if months, _ := mon.ListMonths(ctx, absChat); len(months) != 3 {
		t.Fatalf("ListMonths = %v, want 3 months (08,12,01)", months)
	}
}

func TestIngestNoMonthlyMode(t *testing.T) {
	mem, _ := newStores(t)
	ctx := context.Background()
	const absChat2 int64 = 2003920475340

	// mon == nil: the membership-only path. Must succeed and persist
	// members without ever touching monthstats.
	res, err := histimport.Ingest(ctx, strings.NewReader(realExport), absChat2, mem, nil, nil, false)
	if err != nil {
		t.Fatalf("no-monthly ingest: %v", err)
	}
	if res.Stats == nil {
		t.Fatal("Result.Stats must be populated even without a MonthlyStore")
	}
	if res.MembersWritten != 2 {
		t.Fatalf("MembersWritten = %d, want 2", res.MembersWritten)
	}
	// No monthly sink ran, so its counters stay zero.
	if res.MonthlyAccepted != 0 || res.MonthlyDeduped != 0 || res.MonthlySkippedLive != 0 {
		t.Fatalf("monthly counters = %d/%d/%d, want all 0 in membership-only mode",
			res.MonthlyAccepted, res.MonthlyDeduped, res.MonthlySkippedLive)
	}
	oleg, err := mem.GetMember(ctx, 100, absChat2)
	if err != nil {
		t.Fatalf("get user100 (no-monthly): %v", err)
	}
	if oleg.MessageCount != 3 || oleg.KnownVia != membership.SourceImport {
		t.Fatalf("user100 = count %d via %q, want 3 / import",
			oleg.MessageCount, oleg.KnownVia)
	}
}
