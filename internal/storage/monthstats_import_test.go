package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/veschin/bidlobot/internal/domain/monthstats"
)

// TestApplyImportRejectsDuplicateMessageIDs verifies the per-chat
// imported-message-ID index: sending the SAME batch twice with the same
// message IDs must NOT double-count messages.
func TestApplyImportRejectsDuplicateMessageIDs(t *testing.T) {
	repo := newMonthRepo(t)
	ctx := context.Background()
	const absChat = 200
	month := "2026-05"
	now := time.Now().UTC()

	// Identical batch sent twice (simulating a crash + retry where the
	// importer re-sends the same message IDs).
	batch := map[monthstats.FlushKey]*monthstats.FlushDelta{
		{AbsChatID: absChat, Month: month, UserID: monthstats.MetaUserID}: {
			MsgDelta: 5, RuneDelta: 100,
			LongestUserID: 10, LongestRunes: 50,
			LongestExcerpt: "hello", LongestFull: false,
		},
		{AbsChatID: absChat, Month: month, UserID: 10}: {
			MsgDelta: 5, RuneDelta: 100,
			FirstSeen: now,
		},
	}
	state1 := &monthstats.MonthState{
		AbsChatID: absChat,
		UpdatedAt: now,
	}
	ids := []int64{1001, 1002, 1003, 1004, 1005}
	if err := repo.ApplyImport(ctx, batch, state1, ids); err != nil {
		t.Fatal(err)
	}

	state2 := &monthstats.MonthState{
		AbsChatID: absChat,
		UpdatedAt: now.Add(time.Second),
	}
	if err := repo.ApplyImport(ctx, batch, state2, ids); err != nil {
		t.Fatal(err)
	}

	meta, _, err := repo.GetMonth(ctx, absChat, month)
	if err != nil {
		t.Fatal(err)
	}
	if meta == nil {
		t.Fatal("meta is nil after imports")
	}
	if meta.TotalMsgs != 5 {
		t.Fatalf("expected 5 messages with per-ID dedup, got %d", meta.TotalMsgs)
	}
}

// TestSetLiveTrackStartFirstWriteOnly verifies that SetLiveTrackStart
// writes the timestamp only when LiveTrackStart is zero, and preserves
// all other fields (including a previously written ImportHWM).
//
// This already passes and locks the contract: even after the planned
// refactor, the first-write-only semantic must not change.
func TestSetLiveTrackStartFirstWriteOnly(t *testing.T) {
	repo := newMonthRepo(t)
	ctx := context.Background()
	const absChat = 300

	ts1 := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	if err := repo.SetLiveTrackStart(ctx, absChat, ts1); err != nil {
		t.Fatal(err)
	}

	// Second write with different timestamp must be silently ignored.
	ts2 := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	if err := repo.SetLiveTrackStart(ctx, absChat, ts2); err != nil {
		t.Fatal(err)
	}

	st, err := repo.GetState(ctx, absChat)
	if err != nil {
		t.Fatal(err)
	}
	if st == nil {
		t.Fatal("state is nil")
	}
	if !st.LiveTrackStart.Equal(ts1) {
		t.Fatalf("LiveTrackStart must remain %v after second write, got %v", ts1, st.LiveTrackStart)
	}
}

// TestSetLiveTrackStartPreservesImportHWM verifies that SetLiveTrackStart
// does not clobber a previously written ImportHWM in the same MonthState.
func TestSetLiveTrackStartPreservesImportHWM(t *testing.T) {
	repo := newMonthRepo(t)
	ctx := context.Background()
	const absChat = 301

	// Write a state with ImportHWM first (simulating an import before
	// the live tracker started).
	state := &monthstats.MonthState{
		AbsChatID: absChat,
		ImportHWM: 5000,
		UpdatedAt: time.Now().UTC(),
	}
	if err := repo.PutState(ctx, state); err != nil {
		t.Fatal(err)
	}

	ts := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	if err := repo.SetLiveTrackStart(ctx, absChat, ts); err != nil {
		t.Fatal(err)
	}

	st, err := repo.GetState(ctx, absChat)
	if err != nil {
		t.Fatal(err)
	}
	if st.ImportHWM != 5000 {
		t.Fatalf("ImportHWM must be preserved as 5000 after SetLiveTrackStart, got %d", st.ImportHWM)
	}
	if !st.LiveTrackStart.Equal(ts) {
		t.Fatalf("LiveTrackStart must be %v, got %v", ts, st.LiveTrackStart)
	}
}
