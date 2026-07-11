package stats

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"
)

func moscowLoc() *time.Location {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		panic(err)
	}
	return loc
}

// TestGetTodayByChatCrossesMidnightMoscow verifies GetTodayByChat counts
// messages by Europe/Moscow calendar day. A message at 23:59 MSK yesterday
// must NOT count in today's total; a message at 00:01 MSK today MUST count.
func TestGetTodayByChatCrossesMidnightMoscow(t *testing.T) {
	msk := moscowLoc()
	store := newMockStore()
	buf := NewBuffer(store, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	ctx := context.Background()
	const absChat = 100

	nowMSK := time.Now().In(msk)
	yesterday := nowMSK.Add(-24 * time.Hour)
	todayStart := time.Date(nowMSK.Year(), nowMSK.Month(), nowMSK.Day(), 0, 0, 0, 0, msk)

	// 23:59 MSK yesterday
	lateYesterday := time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 23, 59, 0, 0, msk)
	buf.Increment(1, absChat, lateYesterday)

	// 00:01 MSK today
	earlyToday := time.Date(todayStart.Year(), todayStart.Month(), todayStart.Day(), 0, 1, 0, 0, msk)
	buf.Increment(1, absChat, earlyToday)

	total, users := buf.GetTodayByChat(ctx, absChat)
	if total != 1 {
		t.Fatalf("expected 1 message for today (only 00:01 MSK), got %d", total)
	}
	if users != 1 {
		t.Fatalf("expected 1 active user today, got %d", users)
	}
}

// TestGetTodayByChatSurvivesFlush verifies today's count persists across
// a flush (durable daily bucket). After flush the pending buffer is empty
// but the durable daily data must still be visible.
func TestGetTodayByChatSurvivesFlush(t *testing.T) {
	store := newMockStore()
	buf := NewBuffer(store, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	ctx := context.Background()
	const absChat = 101

	now := time.Now()
	buf.Increment(1, absChat, now)
	buf.Increment(2, absChat, now)

	buf.Flush()

	total, users := buf.GetTodayByChat(ctx, absChat)
	if total != 2 {
		t.Fatalf("expected 2 messages today after flush, got %d", total)
	}
	if users != 2 {
		t.Fatalf("expected 2 active users today after flush, got %d", users)
	}
}

// TestGetTodayByChatAfterRestart verifies today's counts survive a buffer
// restart (i.e. a fresh Buffer reads durable daily data from the Store).
func TestGetTodayByChatAfterRestart(t *testing.T) {
	store := newMockStore()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()
	const absChat = 102

	now := time.Now()

	// First buffer lifetime: record and flush.
	buf1 := NewBuffer(store, log)
	buf1.Increment(5, absChat, now)
	buf1.Increment(5, absChat, now)
	buf1.Increment(7, absChat, now)
	buf1.Flush()

	// Second buffer lifetime: fresh buffer, no pending data.
	buf2 := NewBuffer(store, log)

	total, users := buf2.GetTodayByChat(ctx, absChat)
	if total != 3 {
		t.Fatalf("expected 3 messages today after restart, got %d", total)
	}
	if users != 2 {
		t.Fatalf("expected 2 active users today after restart, got %d", users)
	}
}
