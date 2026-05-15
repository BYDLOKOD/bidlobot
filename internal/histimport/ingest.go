package histimport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/domain/monthstats"
)

// MembershipStore is the membership persistence the importer needs.
// *storage.MembershipRepo satisfies it.
type MembershipStore interface {
	UpsertChat(ctx context.Context, c membership.Chat) error
	UpsertMember(ctx context.Context, p membership.MemberPatch) (*membership.Member, error)
}

// MonthlyStore is the monthly persistence the importer needs.
// *storage.MonthStatsRepo satisfies it. ApplyImport applies the additive
// batch AND writes the advanced MonthState in ONE transaction, so a
// crash leaves neither applied and a retry re-skips correctly by the
// unchanged watermark (additive monthly counters are only idempotent
// because of this atomic pairing). Pass a nil MonthlyStore for
// membership-only import.
type MonthlyStore interface {
	GetState(ctx context.Context, absChatID int64) (*monthstats.MonthState, error)
	ApplyImport(ctx context.Context, batch map[monthstats.FlushKey]*monthstats.FlushDelta, state *monthstats.MonthState) error
}

// Result is the ingest outcome, used by both report renderers.
type Result struct {
	Stats              *Stats
	MembersWritten     int
	MonthlyAccepted    int64 // export rows newly counted into monthstats
	MonthlyDeduped     int64 // skipped: id <= prior watermark (already ingested)
	MonthlySkippedLive int64 // skipped: ts >= LiveTrackStart (already counted live)
	PriorWatermark     int64
	NewWatermark       int64
}

// monthlySink accumulates the additive monthstats batch during the
// streaming pass, applying the two idempotency skips. It mirrors
// monthstats.Buffer.Add's user+meta accumulation exactly so import and
// live produce identical aggregates.
type monthlySink struct {
	absChatID   int64
	hwm         int64
	liveStart   time.Time
	batch       map[monthstats.FlushKey]*monthstats.FlushDelta
	accepted    int64
	deduped     int64
	skippedLive int64
}

func (s *monthlySink) OnMessage(ev MessageEvent) error {
	if ev.MessageID > 0 && ev.MessageID <= s.hwm {
		s.deduped++
		return nil
	}
	// LiveTrackStart skip applies ONLY when set: a chat with no live
	// tracking yet (e.g. the bot not added) must import everything, else
	// every row (ts >= zero time) would be skipped.
	if !s.liveStart.IsZero() && !ev.Sample.TS.Before(s.liveStart) {
		s.skippedLive++
		return nil
	}

	sm := ev.Sample
	uk := monthstats.FlushKey{AbsChatID: s.absChatID, Month: sm.Month, UserID: sm.UserID}
	ue := s.batch[uk]
	if ue == nil {
		ue = &monthstats.FlushDelta{FirstSeen: sm.TS}
		s.batch[uk] = ue
	}
	ue.MsgDelta++
	ue.RuneDelta += sm.Runes
	ue.CustomEmoji += sm.CustomEmoji
	ue.Code += sm.Code
	ue.Mention += sm.Mention
	ue.BotCommand += sm.BotCommand
	ue.KeywordDelta += sm.Keyword
	if !sm.TS.IsZero() && (ue.FirstSeen.IsZero() || sm.TS.Before(ue.FirstSeen)) {
		ue.FirstSeen = sm.TS
	}

	mk := monthstats.FlushKey{AbsChatID: s.absChatID, Month: sm.Month, UserID: monthstats.MetaUserID}
	me := s.batch[mk]
	if me == nil {
		me = &monthstats.FlushDelta{}
		s.batch[mk] = me
	}
	me.MsgDelta++
	me.RuneDelta += sm.Runes
	if sm.Runes > me.LongestRunes {
		me.LongestRunes = sm.Runes
		me.LongestUserID = sm.UserID
		me.LongestExcerpt = sm.Excerpt
		me.LongestFull = sm.ExcerptFull
	}
	s.accepted++
	return nil
}

// Ingest runs the single streaming pass then persists membership (always)
// and monthstats (when mon != nil). It is the one entry point shared by
// the CLI and the DM flow; the caller owns the DB handle (no bbolt open
// here), which is exactly why an in-process DM import has no flock
// problem. progress(done,total) is called during the membership write
// loop (nil = silent).
func Ingest(ctx context.Context, r io.Reader, absChatID int64, mem MembershipStore, mon MonthlyStore, progress func(done, total int), verbose bool) (*Result, error) {
	var sink *monthlySink
	var state *monthstats.MonthState
	if mon != nil {
		st, err := mon.GetState(ctx, absChatID)
		if err != nil && !errors.Is(err, monthstats.ErrNotFound) {
			return nil, fmt.Errorf("load month state: %w", err)
		}
		if st == nil {
			st = &monthstats.MonthState{AbsChatID: absChatID}
		}
		state = st
		sink = &monthlySink{
			absChatID: absChatID,
			hwm:       st.ImportHWM,
			liveStart: st.LiveTrackStart,
			batch:     make(map[monthstats.FlushKey]*monthstats.FlushDelta),
		}
	}

	var ms MessageSink
	if sink != nil {
		ms = sink
	}
	stats, err := Parse(ctx, r, ms, verbose)
	if err != nil {
		return nil, err
	}

	if err := mem.UpsertChat(ctx, membership.Chat{
		AbsChatID:    absChatID,
		Title:        stats.ChatName,
		Type:         stats.ChatType,
		InstalledAt:  stats.Earliest,
		LastUpdateAt: time.Now().UTC(),
	}); err != nil {
		return nil, fmt.Errorf("upsert chat: %w", err)
	}

	written := 0
	total := len(stats.Users)
	i := 0
	for _, a := range stats.Users {
		i++
		first := a.FirstName
		joined := a.JoinedAt
		if joined.IsZero() {
			joined = a.MinTS
		}
		cnt := a.Count
		if _, err := mem.UpsertMember(ctx, membership.MemberPatch{
			UserID:          a.UserID,
			AbsChatID:       absChatID,
			FirstName:       &first,
			Status:          membership.StatusMember,
			KnownVia:        membership.SourceImport,
			JoinedAt:        joined,
			LastMessageAt:   a.MaxTS,
			SetMessageCount: &cnt,
			Now:             a.MaxTS,
		}); err != nil {
			return nil, fmt.Errorf("upsert member %d: %w", a.UserID, err)
		}
		written++
		if progress != nil && (i%500 == 0 || i == total) {
			progress(i, total)
		}
	}

	res := &Result{Stats: stats, MembersWritten: written}
	if sink != nil {
		res.MonthlyAccepted = sink.accepted
		res.MonthlyDeduped = sink.deduped
		res.MonthlySkippedLive = sink.skippedLive
		res.PriorWatermark = state.ImportHWM

		ns := *state
		ns.AbsChatID = absChatID
		if stats.MaxMessageID > ns.ImportHWM {
			ns.ImportHWM = stats.MaxMessageID
		}
		if stats.MinMessageID > 0 && (ns.ImportMinID == 0 || stats.MinMessageID < ns.ImportMinID) {
			ns.ImportMinID = stats.MinMessageID
		}
		if stats.Latest.After(ns.ImportMaxTS) {
			ns.ImportMaxTS = stats.Latest
		}
		if ns.Sealed == nil {
			ns.Sealed = make(map[string]bool)
		}
		frontier := ns.ImportMaxTS.UTC().Format("2006-01")
		for k := range sink.batch {
			if k.Month < frontier {
				ns.Sealed[k.Month] = true
			}
		}
		ns.UpdatedAt = time.Now().UTC()
		res.NewWatermark = ns.ImportHWM

		if err := mon.ApplyImport(ctx, sink.batch, &ns); err != nil {
			return nil, fmt.Errorf("apply monthly import: %w", err)
		}
	}
	return res, nil
}
