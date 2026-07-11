package stats

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type bufferEntry struct {
	countDelta int64
	firstSeen  time.Time
	lastSeen   time.Time
}

type dailyEntry struct {
	countDelta int64
	lastSeen   time.Time
}

// moscowDay returns the Europe/Moscow calendar day for ts, as "YYYY-MM-DD".
func moscowDay(ts time.Time) string {
	moscow, _ := time.LoadLocation("Europe/Moscow")
	return ts.In(moscow).Format("2006-01-02")
}

// Buffer accumulates lifetime and daily (Moscow-calendar) deltas, flushing
// both atomically to the Store. The daily layer makes GetTodayByChat
// durable across flushes and restarts.
type Buffer struct {
	mu           sync.Mutex
	pending      map[FlushKey]*bufferEntry
	dailyPending map[string]map[FlushKey]*dailyEntry
	store        Store
	log          *slog.Logger
	ticker       *time.Ticker
	stopCh       chan struct{}
}

// NewBuffer создаёт новый буфер со слоём накопления дельт для последующей записи.
func NewBuffer(store Store, log *slog.Logger) *Buffer {
	return &Buffer{
		pending:      make(map[FlushKey]*bufferEntry),
		dailyPending: make(map[string]map[FlushKey]*dailyEntry),
		store:        store,
		log:          log,
		stopCh:       make(chan struct{}),
	}
}

// Increment добавляет единицу к счётчику сообщений для пары (userID, absChatID)
// как в lifetime, так и в Moscow-day статистику.
func (b *Buffer) Increment(userID, absChatID int64, ts time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Lifetime delta
	key := FlushKey{UserID: userID, AbsChatID: absChatID}
	entry, exists := b.pending[key]
	if !exists {
		entry = &bufferEntry{firstSeen: ts}
		b.pending[key] = entry
	}
	entry.countDelta++
	if ts.After(entry.lastSeen) {
		entry.lastSeen = ts
	}

	// Daily delta (Moscow calendar day)
	day := moscowDay(ts)
	dayMap, ok := b.dailyPending[day]
	if !ok {
		dayMap = make(map[FlushKey]*dailyEntry)
		b.dailyPending[day] = dayMap
	}
	de, deExists := dayMap[key]
	if !deExists {
		de = &dailyEntry{}
		dayMap[key] = de
	}
	de.countDelta++
	if de.lastSeen.IsZero() || ts.After(de.lastSeen) {
		de.lastSeen = ts
	}
}

// Run запускает горутину с периодическим сбросом буфера.
// Завершение вызывается по закрытии ctx или явному Stop.
func (b *Buffer) Run(ctx context.Context, interval time.Duration) {
	b.ticker = time.NewTicker(interval)
	go func() {
		for {
			select {
			case <-b.ticker.C:
				b.flush(ctx)
			case <-b.stopCh:
				b.ticker.Stop()
				b.flush(ctx)
				return
			case <-ctx.Done():
				b.ticker.Stop()
				b.flush(ctx)
				return
			}
		}
	}()
}

// flush performs an atomic swap: lock -> swap pending -> unlock -> store.FlushAtomic.
// On error, deltas are re-queued into the buffer additively.
func (b *Buffer) flush(ctx context.Context) {
	b.mu.Lock()
	toFlush := b.pending
	b.pending = make(map[FlushKey]*bufferEntry)
	toFlushDaily := b.dailyPending
	b.dailyPending = make(map[string]map[FlushKey]*dailyEntry)
	b.mu.Unlock()
	if len(toFlush) == 0 && len(toFlushDaily) == 0 {
		return
	}

	batch := make(map[FlushKey]*FlushDelta)
	for key, entry := range toFlush {
		batch[key] = &FlushDelta{
			CountDelta: entry.countDelta,
			FirstSeen:  entry.firstSeen,
			LastSeen:   entry.lastSeen,
		}
	}
	dailyBatch := make(map[string]map[FlushKey]*FlushDelta)
	for day, dayMap := range toFlushDaily {
		db := make(map[FlushKey]*FlushDelta, len(dayMap))
		for key, de := range dayMap {
			db[key] = &FlushDelta{
				CountDelta: de.countDelta,
				LastSeen:   de.lastSeen,
			}
		}
		dailyBatch[day] = db
	}
	if err := b.store.FlushAtomic(ctx, batch, dailyBatch); err != nil {
		// Restore deltas on failure - both lifetime and daily,
		// since nothing was committed atomically.
		b.mu.Lock()
		for key, entry := range toFlush {
			if existing, ok := b.pending[key]; ok {
				existing.countDelta += entry.countDelta
				if entry.firstSeen.Before(existing.firstSeen) {
					existing.firstSeen = entry.firstSeen
				}
				if entry.lastSeen.After(existing.lastSeen) {
					existing.lastSeen = entry.lastSeen
				}
			} else {
				b.pending[key] = entry
			}
		}
		for day, dayMap := range toFlushDaily {
			dailyMap, ok := b.dailyPending[day]
			if !ok {
				dailyMap = make(map[FlushKey]*dailyEntry)
				b.dailyPending[day] = dailyMap
			}
			for key, de := range dayMap {
				if existing, ok := dailyMap[key]; ok {
					existing.countDelta += de.countDelta
					if de.lastSeen.After(existing.lastSeen) {
						existing.lastSeen = de.lastSeen
					}
				} else {
					dailyMap[key] = de
				}
			}
		}
		b.mu.Unlock()
		b.log.Error("flush failed", "error", err)
	}
}

// Stop инициирует завершение горутины сброса с финальным сбросом.
func (b *Buffer) Stop() {
	select {
	case b.stopCh <- struct{}{}:
	default:
	}
}

// Flush выполняет немедленный сброс буфера в БД.
func (b *Buffer) Flush() {
	b.flush(context.Background())
}

// GetMerged возвращает данные пользователя из БД с наложением текущих значений буфера.
func (b *Buffer) GetMerged(ctx context.Context, userID, absChatID int64) (*Stats, error) {
	dbStats, err := b.store.Get(ctx, userID, absChatID)
	if err != nil && err != ErrNotFound {
		return nil, err
	}

	b.mu.Lock()
	key := FlushKey{UserID: userID, AbsChatID: absChatID}
	entry, exists := b.pending[key]
	b.mu.Unlock()

	if dbStats == nil && !exists {
		return nil, ErrNotFound
	}

	if dbStats == nil {
		dbStats = &Stats{
			UserID:    userID,
			ChatID:    absChatID,
			FirstSeen: entry.firstSeen,
			LastSeen:  entry.lastSeen,
		}
	}

	if exists {
		dbStats.MessageCount += entry.countDelta
		if entry.firstSeen.Before(dbStats.FirstSeen) {
			dbStats.FirstSeen = entry.firstSeen
		}
		if entry.lastSeen.After(dbStats.LastSeen) {
			dbStats.LastSeen = entry.lastSeen
		}
	}

	return dbStats, nil
}

// ListMergedByChat возвращает все записи статистики для чата с наложением буфера.
func (b *Buffer) ListMergedByChat(ctx context.Context, absChatID int64) ([]Stats, error) {
	dbStats, err := b.store.ListByChat(ctx, absChatID)
	if err != nil {
		return nil, err
	}

	b.mu.Lock()
	mergeMap := make(map[int64]*Stats)
	for i := range dbStats {
		userID := dbStats[i].UserID
		mergeMap[userID] = &dbStats[i]
	}

	for key, entry := range b.pending {
		if key.AbsChatID != absChatID {
			continue
		}
		if s, ok := mergeMap[key.UserID]; ok {
			s.MessageCount += entry.countDelta
			if entry.firstSeen.Before(s.FirstSeen) {
				s.FirstSeen = entry.firstSeen
			}
			if entry.lastSeen.After(s.LastSeen) {
				s.LastSeen = entry.lastSeen
			}
		} else {
			mergeMap[key.UserID] = &Stats{
				UserID:       key.UserID,
				ChatID:       key.AbsChatID,
				MessageCount: entry.countDelta,
				FirstSeen:    entry.firstSeen,
				LastSeen:     entry.lastSeen,
			}
		}
	}
	b.mu.Unlock()

	results := make([]Stats, 0, len(mergeMap))
	for _, s := range mergeMap {
		results = append(results, *s)
	}
	return results, nil
}

// GetTodayByChat возвращает число сообщений и уникальных пользователей
// за текущий день по Europe/Moscow, читая durable daily данные из БД
// и объединяя с незаписанными daily дельтами из буфера.
func (b *Buffer) GetTodayByChat(ctx context.Context, absChatID int64) (totalMsgs, activeUsers int64) {
	moscow, _ := time.LoadLocation("Europe/Moscow")
	todayStr := time.Now().In(moscow).Format("2006-01-02")

	seen := make(map[int64]bool)

	// Durable daily данные из БД.
	durable, err := b.store.GetDaily(ctx, absChatID, todayStr)
	if err == nil {
		for _, s := range durable {
			totalMsgs += s.MessageCount
			seen[s.UserID] = true
		}
	}

	// Незаписанные daily дельты из буфера.
	b.mu.Lock()
	if dayMap, ok := b.dailyPending[todayStr]; ok {
		for key, de := range dayMap {
			if key.AbsChatID == absChatID {
				totalMsgs += de.countDelta
				seen[key.UserID] = true
			}
		}
	}
	b.mu.Unlock()

	activeUsers = int64(len(seen))
	return
}
