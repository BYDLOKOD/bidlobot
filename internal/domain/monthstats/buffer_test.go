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
// is purely additive on Flush (idempotency is the importer's job, never
// the store's) so it can also prove the additive contract directly.
type memStore struct {
	mu       sync.Mutex
	meta     map[string]*MonthMeta        // key: chat|month
	users    map[string]*MonthUserStat    // key: chat|month|uid
	months   map[int64]map[string]bool    // chat -> set of months
	state    map[int64]*MonthState        // chat -> state
	summary  map[string]*MonthSummary     // key: chat|month
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

func cm(chat int64, month string) string  { return string(rune(chat)) + "|" + month }
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

func (s *memStore) PutState(_ context.Context, st *MonthState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := *st
	s.state[st.AbsChatID] = &c
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

func TestBufferLiveTrackStartPersistedOnce(t *testing.T) {
	st := newMemStore()
	b := NewBuffer(st, testLogger())
	early := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)
	s1 := sample(100, 1, "2026-04", 5)
	s1.TS = late
	b.Add(s1)
	s2 := sample(100, 2, "2026-04", 5)
	s2.TS = early
	b.Add(s2)
	b.Flush()

	got, err := st.GetState(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if !got.LiveTrackStart.Equal(early) {
		t.Fatalf("LiveTrackStart = %v, want earliest %v", got.LiveTrackStart, early)
	}
	// A second flush must not move it.
	s3 := sample(100, 3, "2026-04", 5)
	s3.TS = time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	b.Add(s3)
	b.Flush()
	got2, _ := st.GetState(context.Background(), 100)
	if !got2.LiveTrackStart.Equal(early) {
		t.Fatalf("LiveTrackStart moved after second flush: %v", got2.LiveTrackStart)
	}
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
