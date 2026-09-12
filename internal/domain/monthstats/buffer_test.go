package monthstats

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sort"
	"sync"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// memStore is an in-memory monthstats.Store for buffer/service tests. It
// is purely additive on Flush so it can also prove the additive contract
// directly.
type memStore struct {
	mu       sync.Mutex
	meta     map[string]*MonthMeta     // key: chat|month
	users    map[string]*MonthUserStat // key: chat|month|uid
	months   map[int64]map[string]bool // chat -> set of months
	state    map[int64]*MonthState     // chat -> state
	summary  map[string]*MonthSummary  // key: chat|month
	flushErr error
	flushCnt int
}

func newMemStore() *memStore {
	return &memStore{
		meta:    map[string]*MonthMeta{},
		users:   map[string]*MonthUserStat{},
		months:  map[int64]map[string]bool{},
		state:   map[int64]*MonthState{},
		summary: map[string]*MonthSummary{},
	}
}

func cm(chat int64, month string) string { return string(rune(chat)) + "|" + month }
func cmu(chat int64, m string, u int64) string {
	return string(rune(chat)) + "|" + m + "|" + string(rune(u))
}

func (s *memStore) GetMonth(_ context.Context, chat int64, month string) (*MonthMeta, []MonthUserStat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var meta *MonthMeta
	if m, ok := s.meta[cm(chat, month)]; ok {
		c := *m
		meta = &c
	}
	var out []MonthUserStat
	for _, u := range s.users {
		if u.AbsChatID == chat && u.Month == month {
			out = append(out, *u)
		}
	}
	return meta, out, nil
}

func (s *memStore) ListMonths(_ context.Context, chat int64) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for m := range s.months[chat] {
		out = append(out, m)
	}
	sort.Strings(out)
	return out, nil
}

func (s *memStore) GetState(_ context.Context, chat int64) (*MonthState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.state[chat]
	if !ok {
		return nil, ErrNotFound
	}
	c := *st
	return &c, nil
}

// SetLiveTrackStart mirrors MonthStatsRepo.SetLiveTrackStart: the
// boundary is the MINIMUM observed timestamp, and UpdatedAt moves only
// when a write actually lands. The eager first-Add persist and the flush
// both call it, so the double has to accept a LATER write followed by an
// EARLIER one - a double that refuses every write after the first hides
// the exact ordering the flush depends on.
func (s *memStore) SetLiveTrackStart(_ context.Context, chat int64, ts time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.state[chat]
	if !ok {
		st = &MonthState{AbsChatID: chat}
		s.state[chat] = st
	}
	if !st.LiveTrackStart.IsZero() && !ts.Before(st.LiveTrackStart) {
		return nil // existing is earlier or the same; a later boundary never moves it
	}
	st.LiveTrackStart = ts
	st.UpdatedAt = time.Now().UTC()
	return nil
}

func (s *memStore) GetSummary(_ context.Context, chat int64, month string) (*MonthSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sm, ok := s.summary[cm(chat, month)]
	if !ok {
		return nil, ErrNotFound
	}
	c := *sm
	return &c, nil
}

func (s *memStore) PutSummary(_ context.Context, sm *MonthSummary) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := *sm
	s.summary[cm(sm.AbsChatID, sm.Month)] = &c
	return nil
}

func (s *memStore) Flush(_ context.Context, batch map[FlushKey]*FlushDelta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushCnt++
	if s.flushErr != nil {
		return s.flushErr
	}
	for k, d := range batch {
		if s.months[k.AbsChatID] == nil {
			s.months[k.AbsChatID] = map[string]bool{}
		}
		s.months[k.AbsChatID][k.Month] = true
		if k.UserID == MetaUserID {
			m := s.meta[cm(k.AbsChatID, k.Month)]
			if m == nil {
				m = &MonthMeta{AbsChatID: k.AbsChatID, Month: k.Month}
				s.meta[cm(k.AbsChatID, k.Month)] = m
			}
			m.TotalMsgs += d.MsgDelta
			m.TotalRunes += d.RuneDelta
			if d.LongestRunes > m.LongestRunes {
				m.LongestRunes = d.LongestRunes
				m.LongestUserID = d.LongestUserID
				m.LongestExcerpt = d.LongestExcerpt
				m.LongestFull = d.LongestFull
			}
			continue
		}
		u := s.users[cmu(k.AbsChatID, k.Month, k.UserID)]
		if u == nil {
			u = &MonthUserStat{AbsChatID: k.AbsChatID, Month: k.Month, UserID: k.UserID, FirstSeen: d.FirstSeen}
			s.users[cmu(k.AbsChatID, k.Month, k.UserID)] = u
		}
		u.MsgCount += d.MsgDelta
		u.RuneCount += d.RuneDelta
		u.CustomEmoji += d.CustomEmoji
		u.Code += d.Code
		u.Mention += d.Mention
		u.BotCommand += d.BotCommand
		u.KeywordCount += d.KeywordDelta
		if !d.FirstSeen.IsZero() && (u.FirstSeen.IsZero() || d.FirstSeen.Before(u.FirstSeen)) {
			u.FirstSeen = d.FirstSeen
		}
	}
	return nil
}

func sample(chat, uid int64, month string, runes int64) Sample {
	ts, _ := time.Parse("2006-01", month)
	return Sample{
		AbsChatID: chat, UserID: uid, Month: month, TS: ts,
		Runes: runes, Excerpt: "x", ExcerptFull: true,
	}
}

func TestBufferAddAndMergedRead(t *testing.T) {
	st := newMemStore()
	b := NewBuffer(st, testLogger())

	b.Add(sample(100, 1, "2026-04", 10))
	b.Add(sample(100, 1, "2026-04", 5))
	b.Add(sample(100, 2, "2026-04", 3))

	meta, users, err := b.GetMergedMonth(context.Background(), 100, "2026-04")
	if err != nil {
		t.Fatal(err)
	}
	if meta.TotalMsgs != 3 || meta.TotalRunes != 18 {
		t.Fatalf("meta merge wrong: %+v", meta)
	}
	if len(users) != 2 {
		t.Fatalf("want 2 users, got %d", len(users))
	}
}

func TestBufferFlushAdditive(t *testing.T) {
	st := newMemStore()
	b := NewBuffer(st, testLogger())
	b.Add(sample(100, 1, "2026-04", 10))
	b.Flush()
	b.Add(sample(100, 1, "2026-04", 7))
	b.Flush()

	_, users, _ := st.GetMonth(context.Background(), 100, "2026-04")
	if len(users) != 1 || users[0].MsgCount != 2 || users[0].RuneCount != 17 {
		t.Fatalf("additive flush wrong: %+v", users)
	}
}

func TestBufferRemergeOnFlushError(t *testing.T) {
	st := newMemStore()
	st.flushErr = errors.New("db down")
	b := NewBuffer(st, testLogger())
	b.Add(sample(100, 1, "2026-04", 4))
	b.Flush() // fails, deltas re-merged
	b.Add(sample(100, 1, "2026-04", 6))
	st.flushErr = nil
	b.Flush()

	_, users, _ := st.GetMonth(context.Background(), 100, "2026-04")
	if users[0].MsgCount != 2 || users[0].RuneCount != 10 {
		t.Fatalf("expected nothing lost after failed flush: %+v", users)
	}
}

// TestBufferLiveTrackStartTracksEarliest pins the boundary contract from
// 30_stats.md: LiveTrackStart is the earliest live message ts, because
// the (unwired) importer skips rows with ts >= LiveTrackStart - a boundary
// that sits too late would make those messages reachable by both paths.
//
// Deterministic by construction: the ordering that used to be decided by
// goroutine scheduling (the eager first-Add persist landing before the
// first flush) is forced by waiting for that write to land, so the flush
// is always competing with an already-stored, later boundary.
func TestBufferLiveTrackStartTracksEarliest(t *testing.T) {
	st := newMemStore()
	b := NewBuffer(st, testLogger())
	late := time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)
	early := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	earliest := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	first := sample(100, 1, "2026-04", 5)
	first.TS = late
	b.Add(first)
	// Crash safety: the first Add persists the boundary without waiting
	// for a flush.
	waitForLiveTrackStart(t, st, 100, late)

	second := sample(100, 2, "2026-04", 5)
	second.TS = early
	b.Add(second)
	b.Flush()
	if got := liveTrackStart(t, st, 100); !got.Equal(early) {
		t.Fatalf("LiveTrackStart = %v, want the flush's earlier message %v", got, early)
	}

	third := sample(100, 3, "2026-04", 5)
	third.TS = earliest
	b.Add(third)
	b.Flush()
	if got := liveTrackStart(t, st, 100); !got.Equal(earliest) {
		t.Fatalf("LiveTrackStart = %v, want a still earlier message %v", got, earliest)
	}

	fourth := sample(100, 4, "2026-04", 5)
	fourth.TS = late
	b.Add(fourth)
	b.Flush()
	if got := liveTrackStart(t, st, 100); !got.Equal(earliest) {
		t.Fatalf("LiveTrackStart moved later to %v, want %v", got, earliest)
	}
}

// waitForLiveTrackStart blocks until the eager first-Add persist has
// landed, so a test can compete with a boundary that is already stored.
func waitForLiveTrackStart(t *testing.T, st *memStore, chat int64, want time.Time) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		state, err := st.GetState(context.Background(), chat)
		if err == nil && state.LiveTrackStart.Equal(want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("eager LiveTrackStart persist did not land: state=%+v err=%v", state, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func liveTrackStart(t *testing.T, st *memStore, chat int64) time.Time {
	t.Helper()
	state, err := st.GetState(context.Background(), chat)
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	return state.LiveTrackStart
}

func TestBufferConcurrentAdd(t *testing.T) {
	st := newMemStore()
	b := NewBuffer(st, testLogger())
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.Add(sample(100, 1, "2026-04", 1))
		}()
	}
	wg.Wait()
	meta, _, _ := b.GetMergedMonth(context.Background(), 100, "2026-04")
	if meta.TotalMsgs != 200 {
		t.Fatalf("concurrent add lost updates: %d", meta.TotalMsgs)
	}
}
