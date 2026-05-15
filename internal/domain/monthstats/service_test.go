package monthstats

import (
	"context"
	"strings"
	"testing"
	"time"
)

type fakeResolver map[int64]string

func (f fakeResolver) UserDisplay(_ context.Context, _, uid int64) string { return f[uid] }

// seed writes one user row (via the additive store) for month 2026-04.
func seedUser(t *testing.T, st *memStore, uid, msgs, runes, code, custom, mention, bot, kw int64, firstDay int) {
	t.Helper()
	ts := time.Date(2026, 4, firstDay, 0, 0, 0, 0, time.UTC)
	_ = st.Flush(context.Background(), map[FlushKey]*FlushDelta{
		{AbsChatID: 100, Month: "2026-04", UserID: uid}: {
			MsgDelta: msgs, RuneDelta: runes, Code: code, CustomEmoji: custom,
			Mention: mention, BotCommand: bot, KeywordDelta: kw, FirstSeen: ts,
		},
	})
}

func seedMeta(t *testing.T, st *memStore, totMsgs, totRunes, longUser, longRunes int64, full bool) {
	t.Helper()
	_ = st.Flush(context.Background(), map[FlushKey]*FlushDelta{
		{AbsChatID: 100, Month: "2026-04", UserID: MetaUserID}: {
			MsgDelta: totMsgs, RuneDelta: totRunes,
			LongestUserID: longUser, LongestRunes: longRunes,
			LongestExcerpt: "длинный текст", LongestFull: full,
		},
	})
}

func newSvcAt(st *memStore, month string) *Service {
	svc := NewService(st, NewBuffer(st, testLogger()),
		fakeResolver{1: "@oleg", 2: "@arsenij", 3: "@nikita"}, testLogger())
	svc.now = func() time.Time {
		ts, _ := time.Parse("2006-01", month)
		return ts.UTC()
	}
	return svc
}

func TestRenderNominationsAndPercentages(t *testing.T) {
	st := newMemStore()
	// 9 msgs total; Олег 5 (55%), Арсений 3 (33%), Никита 1 (11%).
	seedUser(t, st, 1, 5, 100, 3, 0, 2, 1, 4, 1)
	seedUser(t, st, 2, 3, 50, 0, 0, 0, 0, 0, 2)
	seedUser(t, st, 3, 1, 5, 0, 0, 0, 0, 0, 3)
	seedMeta(t, st, 9, 155, 1, 1459, false)

	// "past" month -> rendered then memoized.
	svc := newSvcAt(st, "2026-05")
	out, err := svc.MonthReport(context.Background(), 100, "2026-04")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Итоги месяца 2026-04",
		"Самый срущий автор",
		"@oleg - 5 (55%)",    // 5*100/9 = 55 (integer trunc)
		"@arsenij - 3 (33%)", // 3*100/9 = 33
		"@nikita - 1 (11%)",  // 1*100/9 = 11
		"Самое длинное сообщение",
		"@oleg - 1,459 символов <i>(обрезано)</i>", // !full -> truncation note
		"Самый кодирующий автор",                   // code section present (Олег has 3)
		"Самый курсористый тип",                    // keyword section present
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered output missing %q\n---\n%s", want, out)
		}
	}
	// Zero-drop: nobody has custom_emoji -> that nomination must be absent.
	if strings.Contains(out, "Самый емоджинутый автор") {
		t.Errorf("custom_emoji nomination should be dropped when all zero:\n%s", out)
	}
}

func TestRenderTwentyPlusBoundaryStrict(t *testing.T) {
	st := newMemStore()
	seedUser(t, st, 1, 21, 10, 0, 0, 0, 0, 0, 1) // counts
	seedUser(t, st, 2, 20, 10, 0, 0, 0, 0, 0, 2) // excluded (strictly >20)
	seedMeta(t, st, 41, 20, 1, 10, true)
	svc := newSvcAt(st, "2026-05")
	out, _ := svc.MonthReport(context.Background(), 100, "2026-04")
	if !strings.Contains(out, "из них 20+ сообщений: <b>1</b>") {
		t.Errorf("expected exactly 1 user >20 (strict), got:\n%s", out)
	}
}

func TestSealMemoizationAndInvalidation(t *testing.T) {
	st := newMemStore()
	seedUser(t, st, 1, 5, 100, 0, 0, 0, 0, 0, 1)
	seedMeta(t, st, 5, 100, 1, 100, true)
	svc := newSvcAt(st, "2026-05") // 2026-04 is past => memoize

	first, _ := svc.MonthReport(context.Background(), 100, "2026-04")
	if _, err := st.GetSummary(context.Background(), 100, "2026-04"); err != nil {
		t.Fatal("expected summary memoized for a past month")
	}
	// Mutate the underlying store; memo must shield the old render.
	seedUser(t, st, 1, 100, 0, 0, 0, 0, 0, 0, 1)
	cached, _ := svc.MonthReport(context.Background(), 100, "2026-04")
	if cached != first {
		t.Error("past month should return the memoized HTML unchanged")
	}
	// An import advancing state.UpdatedAt invalidates the memo.
	_ = st.PutState(context.Background(), &MonthState{
		AbsChatID: 100, UpdatedAt: time.Now().Add(time.Hour).UTC(),
	})
	rebuilt, _ := svc.MonthReport(context.Background(), 100, "2026-04")
	if rebuilt == first {
		t.Error("memo should be invalidated after a later import (state.UpdatedAt)")
	}
}

func TestInProgressMonthNeverMemoized(t *testing.T) {
	st := newMemStore()
	b := NewBuffer(st, testLogger())
	svc := NewService(st, b, fakeResolver{1: "@oleg"}, testLogger())
	svc.now = func() time.Time { return time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC) }

	b.Add(sample(100, 1, "2026-04", 10))
	out1, _ := svc.MonthReport(context.Background(), 100, "2026-04")
	if !strings.Contains(out1, "идёт сейчас") {
		t.Errorf("in-progress month should be tagged: %s", out1)
	}
	if _, err := st.GetSummary(context.Background(), 100, "2026-04"); err == nil {
		t.Error("in-progress month must NOT be memoized")
	}
	b.Add(sample(100, 1, "2026-04", 10)) // live update visible immediately
	out2, _ := svc.MonthReport(context.Background(), 100, "2026-04")
	if out1 == out2 {
		t.Error("in-progress report should reflect new live messages on each call")
	}
}

func TestDefaultMonthPicksNewestPast(t *testing.T) {
	st := newMemStore()
	seedUser(t, st, 1, 1, 1, 0, 0, 0, 0, 0, 1) // creates 2026-04
	seedMeta(t, st, 1, 1, 1, 1, true)
	svc := newSvcAt(st, "2026-06")
	out, err := svc.MonthReport(context.Background(), 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Итоги месяца 2026-04") {
		t.Errorf("default month should be the newest past month with data:\n%s", out)
	}
}

func TestMonthsListing(t *testing.T) {
	st := newMemStore()
	seedUser(t, st, 1, 1, 1, 0, 0, 0, 0, 0, 1)
	svc := newSvcAt(st, "2026-04")
	out, err := svc.Months(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "<code>2026-04</code>") || !strings.Contains(out, "идёт сейчас") {
		t.Errorf("months listing wrong:\n%s", out)
	}
}
