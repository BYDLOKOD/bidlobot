package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/veschin/bidlobot/internal/domain/monthstats"
	"github.com/veschin/bidlobot/internal/storage"
)

func newMonthRepo(t *testing.T) *storage.MonthStatsRepo {
	t.Helper()
	return storage.NewMonthStatsRepo(newTestStore(t).DB())
}

func TestMonthStatsAdditiveFlushAndSplit(t *testing.T) {
	repo := newMonthRepo(t)
	ctx := context.Background()
	ts := time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC)

	batch := map[monthstats.FlushKey]*monthstats.FlushDelta{
		{AbsChatID: 100, Month: "2026-04", UserID: 7}: {
			MsgDelta: 2, RuneDelta: 50, Code: 1, Mention: 3, FirstSeen: ts,
		},
		{AbsChatID: 100, Month: "2026-04", UserID: monthstats.MetaUserID}: {
			MsgDelta: 2, RuneDelta: 50, LongestUserID: 7, LongestRunes: 40,
			LongestExcerpt: "hi", LongestFull: true,
		},
	}
	if err := repo.Flush(ctx, batch); err != nil {
		t.Fatal(err)
	}
	// Flush again: the repo is purely additive (dedup is the importer's
	// job, never the store's) - counts must double.
	if err := repo.Flush(ctx, batch); err != nil {
		t.Fatal(err)
	}

	meta, users, err := repo.GetMonth(ctx, 100, "2026-04")
	if err != nil {
		t.Fatal(err)
	}
	if meta == nil || meta.TotalMsgs != 4 || meta.TotalRunes != 100 {
		t.Fatalf("meta additive wrong: %+v", meta)
	}
	if meta.LongestRunes != 40 || meta.LongestUserID != 7 {
		t.Fatalf("longest max-reduction wrong: %+v", meta)
	}
	if len(users) != 1 {
		t.Fatalf("want 1 user row (meta must be split out, not returned as a user), got %d", len(users))
	}
	u := users[0]
	if u.UserID != 7 || u.MsgCount != 4 || u.RuneCount != 100 || u.Code != 2 || u.Mention != 6 {
		t.Fatalf("user additive wrong: %+v", u)
	}
	if !u.FirstSeen.Equal(ts) {
		t.Fatalf("FirstSeen not preserved: %v", u.FirstSeen)
	}
}

func TestMonthStatsListMonthsParsesMonthSegment(t *testing.T) {
	repo := newMonthRepo(t)
	ctx := context.Background()
	for _, m := range []string{"2025-12", "2026-01", "2026-04"} {
		if err := repo.Flush(ctx, map[monthstats.FlushKey]*monthstats.FlushDelta{
			{AbsChatID: 100, Month: m, UserID: 1}: {MsgDelta: 1},
		}); err != nil {
			t.Fatal(err)
		}
	}
	// A different chat must not bleed in.
	_ = repo.Flush(ctx, map[monthstats.FlushKey]*monthstats.FlushDelta{
		{AbsChatID: 999, Month: "2024-01", UserID: 1}: {MsgDelta: 1},
	})

	months, err := repo.ListMonths(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2025-12", "2026-01", "2026-04"}
	if len(months) != 3 {
		t.Fatalf("want %v, got %v", want, months)
	}
	for i := range want {
		if months[i] != want[i] {
			t.Fatalf("month %d: parsed %q (the YYYY-MM segment must NOT go through parseID), want %q", i, months[i], want[i])
		}
	}
}

func TestMonthStatsStateAndSummaryRoundTrip(t *testing.T) {
	repo := newMonthRepo(t)
	ctx := context.Background()

	if _, err := repo.GetState(ctx, 100); !errors.Is(err, monthstats.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing state, got %v", err)
	}
	st := &monthstats.MonthState{
		AbsChatID: 100, ImportHWM: 5000,
		Sealed:    map[string]bool{"2026-03": true},
		UpdatedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := repo.PutState(ctx, st); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetState(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got.ImportHWM != 5000 || !got.Sealed["2026-03"] {
		t.Fatalf("state round-trip wrong: %+v", got)
	}

	if _, err := repo.GetSummary(ctx, 100, "2026-03"); !errors.Is(err, monthstats.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing summary, got %v", err)
	}
	sum := &monthstats.MonthSummary{AbsChatID: 100, Month: "2026-03", HTML: "<b>x</b>", SchemaVer: monthstats.SummarySchemaVer}
	if err := repo.PutSummary(ctx, sum); err != nil {
		t.Fatal(err)
	}
	gs, err := repo.GetSummary(ctx, 100, "2026-03")
	if err != nil {
		t.Fatal(err)
	}
	if gs.HTML != "<b>x</b>" || gs.SchemaVer != monthstats.SummarySchemaVer {
		t.Fatalf("summary round-trip wrong: %+v", gs)
	}
}
