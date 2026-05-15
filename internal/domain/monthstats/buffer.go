package monthstats

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// bufferEntry accumulates deltas for one FlushKey since the last flush.
// For a user key only the counter fields + firstSeen are used; for the
// MetaUserID key, msg/runes carry the month totals and the longest*
// fields carry the longest-message candidate. This dual use mirrors
// FlushDelta and keeps the buffer one flat map like stats.Buffer.
type bufferEntry struct {
	msg, runes                        int64
	custom, code, mention, botcmd, kw int64
	firstSeen                         time.Time
	longestRunes, longestUser         int64
	longestExcerpt                    string
	longestFull                       bool
}

// Buffer is the live accumulation layer. It copies stats.Buffer's proven
// design verbatim: a mutex-guarded pending map, a periodic atomic
// swap+flush, additive re-merge on flush error, and DB+buffer merged
// reads so the in-progress month is never stale. Idempotent dedup is NOT
// the buffer's concern (each live message is Add-ed exactly once); the
// importer owns dedup via MonthState.
type Buffer struct {
	mu      sync.Mutex
	pending map[FlushKey]*bufferEntry
	store   Store
	log     *slog.Logger
	ticker  *time.Ticker
	stopCh  chan struct{}

	// liveStart tracks the earliest live message ts per chat so the first
	// flush can persist MonthState.LiveTrackStart - the boundary the
	// importer uses to avoid double-counting messages already seen live.
	liveStart     map[int64]time.Time
	liveStartDone map[int64]bool
}

func NewBuffer(store Store, log *slog.Logger) *Buffer {
	return &Buffer{
		pending:       make(map[FlushKey]*bufferEntry),
		store:         store,
		log:           log,
		stopCh:        make(chan struct{}),
		liveStart:     make(map[int64]time.Time),
		liveStartDone: make(map[int64]bool),
	}
}

// Add records one counted live message. Excluded messages are filtered by
// ExtractSample before this is called.
func (b *Buffer) Add(s Sample) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if ls, ok := b.liveStart[s.AbsChatID]; !ok || s.TS.Before(ls) {
		b.liveStart[s.AbsChatID] = s.TS
	}

	uk := FlushKey{AbsChatID: s.AbsChatID, Month: s.Month, UserID: s.UserID}
	ue := b.pending[uk]
	if ue == nil {
		ue = &bufferEntry{firstSeen: s.TS}
		b.pending[uk] = ue
	}
	ue.msg++
	ue.runes += s.Runes
	ue.custom += s.CustomEmoji
	ue.code += s.Code
	ue.mention += s.Mention
	ue.botcmd += s.BotCommand
	ue.kw += s.Keyword
	if !s.TS.IsZero() && (ue.firstSeen.IsZero() || s.TS.Before(ue.firstSeen)) {
		ue.firstSeen = s.TS
	}

	mk := FlushKey{AbsChatID: s.AbsChatID, Month: s.Month, UserID: MetaUserID}
	me := b.pending[mk]
	if me == nil {
		me = &bufferEntry{}
		b.pending[mk] = me
	}
	me.msg++            // -> TotalMsgs
	me.runes += s.Runes // -> TotalRunes
	if s.Runes > me.longestRunes {
		me.longestRunes = s.Runes
		me.longestUser = s.UserID
		me.longestExcerpt = s.Excerpt
		me.longestFull = s.ExcerptFull
	}
}

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

func toDelta(key FlushKey, e *bufferEntry) *FlushDelta {
	if key.UserID == MetaUserID {
		return &FlushDelta{
			MsgDelta:       e.msg,
			RuneDelta:      e.runes,
			LongestUserID:  e.longestUser,
			LongestRunes:   e.longestRunes,
			LongestExcerpt: e.longestExcerpt,
			LongestFull:    e.longestFull,
		}
	}
	return &FlushDelta{
		MsgDelta:     e.msg,
		RuneDelta:    e.runes,
		CustomEmoji:  e.custom,
		Code:         e.code,
		Mention:      e.mention,
		BotCommand:   e.botcmd,
		KeywordDelta: e.kw,
		FirstSeen:    e.firstSeen,
	}
}

// flush: lock -> swap pending -> unlock -> store.Flush. On error the
// deltas are merged back additively (longest = max, firstSeen = min) so
// nothing is lost, exactly like stats.Buffer.flush.
func (b *Buffer) flush(ctx context.Context) {
	b.mu.Lock()
	toFlush := b.pending
	b.pending = make(map[FlushKey]*bufferEntry)
	starts := make(map[int64]time.Time, len(b.liveStart))
	for c, t := range b.liveStart {
		if !b.liveStartDone[c] {
			starts[c] = t
		}
	}
	b.mu.Unlock()

	if len(toFlush) == 0 {
		return
	}

	batch := make(map[FlushKey]*FlushDelta, len(toFlush))
	for key, e := range toFlush {
		batch[key] = toDelta(key, e)
	}

	if err := b.store.Flush(ctx, batch); err != nil {
		b.mu.Lock()
		for key, e := range toFlush {
			if ex, ok := b.pending[key]; ok {
				ex.msg += e.msg
				ex.runes += e.runes
				ex.custom += e.custom
				ex.code += e.code
				ex.mention += e.mention
				ex.botcmd += e.botcmd
				ex.kw += e.kw
				if !e.firstSeen.IsZero() && (ex.firstSeen.IsZero() || e.firstSeen.Before(ex.firstSeen)) {
					ex.firstSeen = e.firstSeen
				}
				if e.longestRunes > ex.longestRunes {
					ex.longestRunes = e.longestRunes
					ex.longestUser = e.longestUser
					ex.longestExcerpt = e.longestExcerpt
					ex.longestFull = e.longestFull
				}
			} else {
				b.pending[key] = e
			}
		}
		b.mu.Unlock()
		b.log.Error("monthstats flush failed", "error", err)
		return
	}

	// Flush succeeded: persist LiveTrackStart once per chat. A failure
	// here is non-fatal (retried next flush) and never loses counts.
	for chat, earliest := range starts {
		st, err := b.store.GetState(ctx, chat)
		if err == ErrNotFound {
			st = &MonthState{AbsChatID: chat}
		} else if err != nil {
			continue
		}
		if st.LiveTrackStart.IsZero() {
			st.LiveTrackStart = earliest
			st.UpdatedAt = time.Now().UTC()
			if err := b.store.PutState(ctx, st); err != nil {
				continue
			}
		}
		b.mu.Lock()
		b.liveStartDone[chat] = true
		b.mu.Unlock()
	}
}

func (b *Buffer) Stop() {
	select {
	case b.stopCh <- struct{}{}:
	default:
	}
}

// Flush forces an immediate synchronous flush (used on graceful shutdown
// and in tests).
func (b *Buffer) Flush() { b.flush(context.Background()) }

// GetMergedMonth returns the month's MonthMeta + user rows with the live
// buffer overlaid, so the in-progress month is never stale.
func (b *Buffer) GetMergedMonth(ctx context.Context, absChatID int64, month string) (*MonthMeta, []MonthUserStat, error) {
	meta, users, err := b.store.GetMonth(ctx, absChatID, month)
	if err != nil {
		return nil, nil, err
	}

	byUser := make(map[int64]*MonthUserStat, len(users))
	for i := range users {
		byUser[users[i].UserID] = &users[i]
	}
	if meta == nil {
		meta = &MonthMeta{AbsChatID: absChatID, Month: month}
	}

	b.mu.Lock()
	for key, e := range b.pending {
		if key.AbsChatID != absChatID || key.Month != month {
			continue
		}
		if key.UserID == MetaUserID {
			meta.TotalMsgs += e.msg
			meta.TotalRunes += e.runes
			if e.longestRunes > meta.LongestRunes {
				meta.LongestRunes = e.longestRunes
				meta.LongestUserID = e.longestUser
				meta.LongestExcerpt = e.longestExcerpt
				meta.LongestFull = e.longestFull
			}
			continue
		}
		s, ok := byUser[key.UserID]
		if !ok {
			s = &MonthUserStat{
				AbsChatID: absChatID, Month: month, UserID: key.UserID,
				FirstSeen: e.firstSeen,
			}
			byUser[key.UserID] = s
		}
		s.MsgCount += e.msg
		s.RuneCount += e.runes
		s.CustomEmoji += e.custom
		s.Code += e.code
		s.Mention += e.mention
		s.BotCommand += e.botcmd
		s.KeywordCount += e.kw
		if !e.firstSeen.IsZero() && (s.FirstSeen.IsZero() || e.firstSeen.Before(s.FirstSeen)) {
			s.FirstSeen = e.firstSeen
		}
	}
	b.mu.Unlock()

	out := make([]MonthUserStat, 0, len(byUser))
	for _, s := range byUser {
		out = append(out, *s)
	}
	return meta, out, nil
}

// ListMergedMonths returns every month with data in DB or the live
// buffer, ascending.
func (b *Buffer) ListMergedMonths(ctx context.Context, absChatID int64) ([]string, error) {
	months, err := b.store.ListMonths(ctx, absChatID)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(months))
	for _, m := range months {
		seen[m] = true
	}
	b.mu.Lock()
	for key := range b.pending {
		if key.AbsChatID == absChatID && !seen[key.Month] {
			seen[key.Month] = true
			months = append(months, key.Month)
		}
	}
	b.mu.Unlock()
	// Re-sort: pending months may be out of order vs the sorted DB list.
	for i := 1; i < len(months); i++ {
		for j := i; j > 0 && months[j-1] > months[j]; j-- {
			months[j-1], months[j] = months[j], months[j-1]
		}
	}
	return months, nil
}
