package summarize

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

func e(id int, text string) Entry {
	return Entry{MsgID: id, UserID: int64(id), Name: "u" + strconv.Itoa(id), TS: time.Unix(int64(1700000000+id), 0).UTC(), Text: text}
}

func TestBuffer_CountCapEvictsOldest(t *testing.T) {
	b := NewBuffer(BufferConfig{MaxPerChat: 3, MaxBytesChat: 1 << 20, MaxChats: 8})
	for i := 1; i <= 5; i++ {
		b.Record(10, e(i, "msg"))
	}
	win, total := b.Window(10, 100)
	if total != 3 {
		t.Fatalf("total = %d, want 3 (count cap)", total)
	}
	if len(win) != 3 || win[0].MsgID != 3 || win[2].MsgID != 5 {
		t.Fatalf("window = %+v, want msgIDs 3,4,5 in order", win)
	}
}

func TestBuffer_ByteCapEvictsButKeepsAtLeastOne(t *testing.T) {
	b := NewBuffer(BufferConfig{MaxPerChat: 1000, MaxBytesChat: 10, MaxChats: 8})
	b.Record(7, e(1, "aaaa")) // 4 bytes
	b.Record(7, e(2, "bbbb")) // 8 total
	b.Record(7, e(3, "cccc")) // 12 > 10 -> evict #1 -> 8
	win, total := b.Window(7, 100)
	if total != 2 || win[0].MsgID != 2 || win[1].MsgID != 3 {
		t.Fatalf("window = %+v (total %d), want msgIDs 2,3", win, total)
	}
	// A single message larger than the whole cap is still kept (alone).
	b2 := NewBuffer(BufferConfig{MaxPerChat: 1000, MaxBytesChat: 4, MaxChats: 8})
	b2.Record(7, e(1, "this is way over the four byte cap"))
	if _, tot := b2.Window(7, 10); tot != 1 {
		t.Fatalf("oversized single message total = %d, want 1", tot)
	}
}

func TestBuffer_WindowReturnsCopyChronological(t *testing.T) {
	b := NewBuffer(BufferConfig{MaxPerChat: 10})
	for i := 1; i <= 4; i++ {
		b.Record(1, e(i, "x"))
	}
	win, _ := b.Window(1, 2)
	if len(win) != 2 || win[0].MsgID != 3 || win[1].MsgID != 4 {
		t.Fatalf("last-2 window = %+v, want 3,4", win)
	}
	win[0].Text = "mutated"
	again, _ := b.Window(1, 2)
	if again[0].Text != "x" {
		t.Fatalf("Window must return a copy; backing array was mutated")
	}
}

func TestBuffer_UpdateRewritesTextAndBytes(t *testing.T) {
	b := NewBuffer(BufferConfig{MaxPerChat: 10, MaxBytesChat: 1 << 20})
	b.Record(1, e(1, "old"))
	b.Record(1, e(2, "keep"))
	b.Update(1, 1, "a much longer replacement text")
	win, _ := b.Window(1, 10)
	if win[0].Text != "a much longer replacement text" {
		t.Fatalf("Update did not rewrite text: %q", win[0].Text)
	}
	// Non-existent / evicted message id: no-op, no panic.
	b.Update(1, 99999, "ignored")
	b.Update(2, 1, "wrong chat")
}

func TestBuffer_DistinctChatCapEvictsLRU(t *testing.T) {
	b := NewBuffer(BufferConfig{MaxPerChat: 4, MaxChats: 2})
	b.Record(1, e(1, "a"))
	b.Record(2, e(1, "b"))
	b.Record(3, e(1, "c")) // third distinct chat -> evict LRU (chat 1)
	if _, tot := b.Window(1, 10); tot != 0 {
		t.Fatalf("chat 1 should have been LRU-evicted, total = %d", tot)
	}
	if _, tot := b.Window(3, 10); tot != 1 {
		t.Fatalf("chat 3 should be present, total = %d", tot)
	}
}

func TestBuffer_ConcurrentRecordAndWindow(t *testing.T) {
	b := NewBuffer(BufferConfig{MaxPerChat: 200, MaxBytesChat: 1 << 20, MaxChats: 16})
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				b.Record(int64(g%3), e(i, "concurrent"))
				if i%50 == 0 {
					_, _ = b.Window(int64(g%3), 10)
				}
			}
		}(g)
	}
	wg.Wait()
	for c := int64(0); c < 3; c++ {
		if _, tot := b.Window(c, 1000); tot == 0 || tot > 200 {
			t.Fatalf("chat %d total = %d, want 1..200", c, tot)
		}
	}
}
