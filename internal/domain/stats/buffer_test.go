package stats

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"
)

type mockStore struct {
	mu            sync.Mutex
	data          map[FlushKey]*Stats
	daily         map[string]map[FlushKey]*Stats // day -> key -> stats
	flushErr      error
	flushDailyErr error
	flushCnt      int
}

func newMockStore() *mockStore {
	return &mockStore{
		data:  make(map[FlushKey]*Stats),
		daily: make(map[string]map[FlushKey]*Stats),
	}
}

func (m *mockStore) Get(_ context.Context, userID, absChatID int64) (*Stats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := FlushKey{UserID: userID, AbsChatID: absChatID}
	s, ok := m.data[k]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *s
	return &cp, nil
}

func (m *mockStore) ListByChat(_ context.Context, absChatID int64) ([]Stats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []Stats
	for k, s := range m.data {
		if k.AbsChatID == absChatID {
			result = append(result, *s)
		}
	}
	return result, nil
}

func (m *mockStore) Flush(_ context.Context, batch map[FlushKey]*FlushDelta) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.flushCnt++
	if m.flushErr != nil {
		return m.flushErr
	}
	for k, d := range batch {
		if s, ok := m.data[k]; ok {
			s.MessageCount += d.CountDelta
			if d.LastSeen.After(s.LastSeen) {
				s.LastSeen = d.LastSeen
			}
		} else {
			m.data[k] = &Stats{
				UserID:       k.UserID,
				ChatID:       k.AbsChatID,
				MessageCount: d.CountDelta,
				FirstSeen:    d.FirstSeen,
				LastSeen:     d.LastSeen,
			}
		}
	}
	return nil
}

func (m *mockStore) GetDaily(_ context.Context, absChatID int64, day string) (map[int64]*Stats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[int64]*Stats)
	dayMap, ok := m.daily[day]
	if !ok {
		return result, nil
	}
	for k, s := range dayMap {
		if k.AbsChatID == absChatID {
			result[k.UserID] = &Stats{
				UserID:       k.UserID,
				ChatID:       k.AbsChatID,
				MessageCount: s.MessageCount,
				FirstSeen:    s.FirstSeen,
				LastSeen:     s.LastSeen,
			}
		}
	}
	return result, nil
}

func (m *mockStore) FlushDaily(_ context.Context, batch map[FlushKey]*FlushDelta, day string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.flushDailyErr != nil {
		return m.flushDailyErr
	}
	dayMap, ok := m.daily[day]
	if !ok {
		dayMap = make(map[FlushKey]*Stats)
		m.daily[day] = dayMap
	}
	for k, d := range batch {
		if s, ok := dayMap[k]; ok {
			s.MessageCount += d.CountDelta
			if d.LastSeen.After(s.LastSeen) {
				s.LastSeen = d.LastSeen
			}
		} else {
			dayMap[k] = &Stats{
				UserID:       k.UserID,
				ChatID:       k.AbsChatID,
				MessageCount: d.CountDelta,
				FirstSeen:    d.FirstSeen,
				LastSeen:     d.LastSeen,
			}
		}
	}
	return nil
}

func (m *mockStore) FlushAtomic(_ context.Context, lifetime map[FlushKey]*FlushDelta, daily map[string]map[FlushKey]*FlushDelta) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.flushCnt++
	if m.flushErr != nil {
		return m.flushErr
	}
	// Flush lifetime
	for k, d := range lifetime {
		if s, ok := m.data[k]; ok {
			s.MessageCount += d.CountDelta
			if d.LastSeen.After(s.LastSeen) {
				s.LastSeen = d.LastSeen
			}
		} else {
			m.data[k] = &Stats{
				UserID:       k.UserID,
				ChatID:       k.AbsChatID,
				MessageCount: d.CountDelta,
				FirstSeen:    d.FirstSeen,
				LastSeen:     d.LastSeen,
			}
		}
	}
	// Flush daily
	for day, dayMap := range daily {
		dailyMap, ok := m.daily[day]
		if !ok {
			dailyMap = make(map[FlushKey]*Stats)
			m.daily[day] = dailyMap
		}
		for k, d := range dayMap {
			if s, ok := dailyMap[k]; ok {
				s.MessageCount += d.CountDelta
				if d.LastSeen.After(s.LastSeen) {
					s.LastSeen = d.LastSeen
				}
			} else {
				dailyMap[k] = &Stats{
					UserID:       k.UserID,
					ChatID:       k.AbsChatID,
					MessageCount: d.CountDelta,
					FirstSeen:    d.FirstSeen,
					LastSeen:     d.LastSeen,
				}
			}
		}
	}
	return nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestBufferIncrement(t *testing.T) {
	store := newMockStore()
	buf := NewBuffer(store, testLogger())

	now := time.Now()
	buf.Increment(111, 100, now)
	buf.Increment(111, 100, now.Add(time.Second))
	buf.Increment(222, 100, now)

	s, err := buf.GetMerged(context.Background(), 111, 100)
	if err != nil {
		t.Fatal(err)
	}
	if s.MessageCount != 2 {
		t.Fatalf("expected 2, got %d", s.MessageCount)
	}
}

func TestBufferFlush(t *testing.T) {
	store := newMockStore()
	buf := NewBuffer(store, testLogger())

	now := time.Now()
	buf.Increment(111, 100, now)
	buf.Increment(111, 100, now)
	buf.Flush()

	s, err := store.Get(context.Background(), 111, 100)
	if err != nil {
		t.Fatal(err)
	}
	if s.MessageCount != 2 {
		t.Fatalf("expected 2 in store after flush, got %d", s.MessageCount)
	}

	_, err = buf.GetMerged(context.Background(), 111, 100)
	if err != nil {
		t.Fatal("should still find via store after flush:", err)
	}
}

func TestBufferFlushRemergeOnError(t *testing.T) {
	store := newMockStore()
	store.flushErr = errors.New("db error")
	buf := NewBuffer(store, testLogger())

	now := time.Now()
	buf.Increment(111, 100, now)
	buf.Increment(111, 100, now)
	buf.Flush()

	buf.Increment(111, 100, now)

	s, err := buf.GetMerged(context.Background(), 111, 100)
	if err != nil {
		t.Fatal(err)
	}
	if s.MessageCount != 3 {
		t.Fatalf("expected 3 after failed flush + new increment, got %d", s.MessageCount)
	}
}

func TestBufferMergeDBAndBuffer(t *testing.T) {
	store := newMockStore()
	store.data[FlushKey{UserID: 111, AbsChatID: 100}] = &Stats{
		UserID: 111, ChatID: 100, MessageCount: 50,
		FirstSeen: time.Now().Add(-24 * time.Hour),
		LastSeen:  time.Now().Add(-time.Hour),
	}

	buf := NewBuffer(store, testLogger())
	buf.Increment(111, 100, time.Now())

	s, err := buf.GetMerged(context.Background(), 111, 100)
	if err != nil {
		t.Fatal(err)
	}
	if s.MessageCount != 51 {
		t.Fatalf("expected 51 (50 DB + 1 buffer), got %d", s.MessageCount)
	}
}

func TestBufferListMergedByChat(t *testing.T) {
	store := newMockStore()
	store.data[FlushKey{UserID: 111, AbsChatID: 100}] = &Stats{
		UserID: 111, ChatID: 100, MessageCount: 10,
		FirstSeen: time.Now(), LastSeen: time.Now(),
	}

	buf := NewBuffer(store, testLogger())
	buf.Increment(222, 100, time.Now())

	list, err := buf.ListMergedByChat(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 users, got %d", len(list))
	}
}

func TestBufferConcurrentIncrement(t *testing.T) {
	store := newMockStore()
	buf := NewBuffer(store, testLogger())

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf.Increment(111, 100, time.Now())
		}()
	}
	wg.Wait()

	s, err := buf.GetMerged(context.Background(), 111, 100)
	if err != nil {
		t.Fatal(err)
	}
	if s.MessageCount != 100 {
		t.Fatalf("expected 100, got %d", s.MessageCount)
	}
}

func TestBufferNotFoundBeforeAnyActivity(t *testing.T) {
	store := newMockStore()
	buf := NewBuffer(store, testLogger())

	_, err := buf.GetMerged(context.Background(), 999, 100)
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestBufferFlushDailyErrorDoesNotDuplicateLifetime verifies that a
// FlushAtomic failure does NOT cause lifetime counts to be duplicated
// on retry. Since the operation is atomic, nothing was committed, and
// re-queued deltas produce exactly the expected count after one retry.
func TestBufferFlushDailyErrorDoesNotDuplicateLifetime(t *testing.T) {
	store := newMockStore()
	store.flushErr = errors.New("atomic flush failed")
	buf := NewBuffer(store, testLogger())
	ctx := context.Background()
	now := time.Now()

	// One message from user 111 in chat 100.
	buf.Increment(111, 100, now)
	buf.Flush()

	// FlushAtomic failed (nothing committed). The recovery re-queued
	// the deltas pending. The merged view should still show 1
	// (store has 0, pending has 1).
	s, err := buf.GetMerged(ctx, 111, 100)
	if err != nil {
		t.Fatal(err)
	}
	if s.MessageCount != 1 {
		t.Fatalf("expected 1 (no duplication after atomic flush failure), got %d", s.MessageCount)
	}
}
