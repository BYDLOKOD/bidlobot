package stats

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/veschin/bidlobot/internal/shared"
)

type bufferEntry struct {
	countDelta int64
	firstSeen  time.Time
	lastSeen   time.Time
}

type Buffer struct {
	mu      sync.Mutex
	pending map[FlushKey]*bufferEntry
	store   Store
	log     *slog.Logger
	ticker  *time.Ticker
	stopCh  chan struct{}
}

// NewBuffer создаёт новый буфер со слоём накопления дельт для последующей записи.
func NewBuffer(store Store, log *slog.Logger) *Buffer {
	return &Buffer{
		pending: make(map[FlushKey]*bufferEntry),
		store:   store,
		log:     log,
		stopCh:  make(chan struct{}),
	}
}

// Increment добавляет единицу к счётчику сообщений для пары (userID, absChatID).
// Время первого и последнего события обновляется при необходимости.
func (b *Buffer) Increment(userID, absChatID int64, ts time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()

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

// flush выполняет атомарный обмен: lock -> swap pending -> unlock -> store.Flush.
// При ошибке записи дельты переносятся обратно в буфер аддитивно.
func (b *Buffer) flush(ctx context.Context) {
	b.mu.Lock()
	toFlush := b.pending
	b.pending = make(map[FlushKey]*bufferEntry)
	b.mu.Unlock()

	if len(toFlush) == 0 {
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

	if err := b.store.Flush(ctx, batch); err != nil {
		// Восстановление дельт при ошибке.
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

// GetTodayByChat подсчитывает сообщения и уникальных пользователей за текущий день (UTC).
// Считает только записи из in-memory буфера с lastSeen >= todayUTC.
// После flush буферные записи обнуляются - "today" показывает активность с последнего flush'а,
// что допустимо по спеке (потеря точности до 60 секунд).
func (b *Buffer) GetTodayByChat(_ context.Context, absChatID int64) (totalMsgs, activeUsers int64) {
	today := shared.TodayUTC()

	b.mu.Lock()
	defer b.mu.Unlock()

	for key, entry := range b.pending {
		if key.AbsChatID != absChatID {
			continue
		}
		if entry.lastSeen.Before(today) {
			continue
		}
		totalMsgs += entry.countDelta
		activeUsers++
	}
	return
}
