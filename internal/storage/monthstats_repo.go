package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/domain/monthstats"
)

var (
	bktMonth        = []byte("stats_month")
	bktMonthIdx     = []byte("stats_month_idx")
	bktMonthState   = []byte("stats_month_state")
	bktMonthSummary = []byte("stats_month_summary")
)

// MonthStatsRepo is the bbolt implementation of monthstats.Store. It
// mirrors StatsRepo: a primary bucket of JSON rows plus a value-less
// index bucket for cheap range scans. The per-(chat,month) MonthMeta
// singleton lives in the SAME primary bucket under the userID 0 sentinel
// so it is fetched in the one month prefix scan and updated in the one
// Flush transaction together with the user rows.
type MonthStatsRepo struct {
	db *bolt.DB
}

func NewMonthStatsRepo(db *bolt.DB) *MonthStatsRepo {
	return &MonthStatsRepo{db: db}
}

func (r *MonthStatsRepo) GetMonth(_ context.Context, absChatID int64, month string) (*monthstats.MonthMeta, []monthstats.MonthUserStat, error) {
	var (
		meta  *monthstats.MonthMeta
		users []monthstats.MonthUserStat
	)
	prefix := MonthStatsMonthPrefix(absChatID, month)
	err := r.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bktMonth).Cursor()
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			parts := bytes.SplitN(k, []byte(":"), 4)
			if len(parts) < 4 {
				continue
			}
			if parseID(parts[3]) == monthstats.MetaUserID {
				var m monthstats.MonthMeta
				if err := json.Unmarshal(v, &m); err != nil {
					return err
				}
				meta = &m
				continue
			}
			var s monthstats.MonthUserStat
			if err := json.Unmarshal(v, &s); err != nil {
				return err
			}
			users = append(users, s)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return meta, users, nil
}

func (r *MonthStatsRepo) ListMonths(_ context.Context, absChatID int64) ([]string, error) {
	var months []string
	prefix := MonthStatsChatIndexPrefix(absChatID)
	err := r.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bktMonthIdx).Cursor()
		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			// key: "msi:<abs20>:<YYYY-MM>" - the month is the raw 3rd
			// segment (it contains '-', so parseID must NOT be used).
			parts := bytes.SplitN(k, []byte(":"), 3)
			if len(parts) < 3 {
				continue
			}
			months = append(months, string(parts[2]))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(months) // "YYYY-MM" sorts chronologically as a string
	return months, nil
}

func (r *MonthStatsRepo) GetState(_ context.Context, absChatID int64) (*monthstats.MonthState, error) {
	var st monthstats.MonthState
	err := r.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(bktMonthState).Get(MonthStatsStateKey(absChatID))
		if data == nil {
			return monthstats.ErrNotFound
		}
		return json.Unmarshal(data, &st)
	})
	if err != nil {
		return nil, err
	}
	return &st, nil
}

func (r *MonthStatsRepo) PutState(_ context.Context, st *monthstats.MonthState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return r.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bktMonthState).Put(MonthStatsStateKey(st.AbsChatID), data)
	})
}

func (r *MonthStatsRepo) GetSummary(_ context.Context, absChatID int64, month string) (*monthstats.MonthSummary, error) {
	var s monthstats.MonthSummary
	err := r.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(bktMonthSummary).Get(MonthStatsSummaryKey(absChatID, month))
		if data == nil {
			return monthstats.ErrNotFound
		}
		return json.Unmarshal(data, &s)
	})
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *MonthStatsRepo) PutSummary(_ context.Context, s *monthstats.MonthSummary) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return r.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bktMonthSummary).Put(MonthStatsSummaryKey(s.AbsChatID, s.Month), data)
	})
}

// Flush applies the batch additively in one transaction. Counters are
// summed; the MonthMeta longest message is a max-reduction (additive-safe
// across flushes); the month index key is ensured for every touched
// (chat, month).
func (r *MonthStatsRepo) Flush(_ context.Context, batch map[monthstats.FlushKey]*monthstats.FlushDelta) error {
	if len(batch) == 0 {
		return nil
	}
	return r.db.Update(func(tx *bolt.Tx) error {
		return applyBatchTx(tx, batch)
	})
}

// ApplyImport applies the additive batch AND writes the advanced
// MonthState in ONE transaction. This atomic pairing is what makes the
// additive monthly counters idempotent: a crash leaves NEITHER applied,
// so a retry re-skips correctly by the unchanged watermark. An empty
// batch still writes the state (a fully-deduped re-import must still
// advance the watermark / UpdatedAt).
func (r *MonthStatsRepo) ApplyImport(_ context.Context, batch map[monthstats.FlushKey]*monthstats.FlushDelta, state *monthstats.MonthState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return r.db.Update(func(tx *bolt.Tx) error {
		if err := applyBatchTx(tx, batch); err != nil {
			return err
		}
		return tx.Bucket(bktMonthState).Put(MonthStatsStateKey(state.AbsChatID), data)
	})
}

// ResetMonthly deletes all monthly data + state + summaries for a chat so
// a clean full re-import can run (monthly is independent of membership /
// lifetime stats, so this is safe). Backs the importer's --reset-monthly.
func (r *MonthStatsRepo) ResetMonthly(_ context.Context, absChatID int64) error {
	return r.db.Update(func(tx *bolt.Tx) error {
		for _, b := range []struct {
			bkt    []byte
			prefix []byte
		}{
			{bktMonth, MonthStatsKey(absChatID, "", 0)[:len(MonthStatsKey(absChatID, "", 0))-len(":00000000000000000000")]},
			{bktMonthIdx, MonthStatsChatIndexPrefix(absChatID)},
			{bktMonthSummary, MonthStatsSummaryKey(absChatID, "")},
		} {
			c := tx.Bucket(b.bkt).Cursor()
			for k, _ := c.Seek(b.prefix); k != nil && bytes.HasPrefix(k, b.prefix); k, _ = c.Next() {
				if err := c.Delete(); err != nil {
					return err
				}
			}
		}
		return tx.Bucket(bktMonthState).Delete(MonthStatsStateKey(absChatID))
	})
}

func applyBatchTx(tx *bolt.Tx, batch map[monthstats.FlushKey]*monthstats.FlushDelta) error {
	rows := tx.Bucket(bktMonth)
	idx := tx.Bucket(bktMonthIdx)

	for key, d := range batch {
		if err := idx.Put(MonthStatsChatIndex(key.AbsChatID, key.Month), nil); err != nil {
			return err
		}
		dbKey := MonthStatsKey(key.AbsChatID, key.Month, key.UserID)

		if key.UserID == monthstats.MetaUserID {
			var m monthstats.MonthMeta
			if existing := rows.Get(dbKey); existing != nil {
				if err := json.Unmarshal(existing, &m); err != nil {
					return err
				}
			} else {
				m.AbsChatID = key.AbsChatID
				m.Month = key.Month
			}
			m.TotalMsgs += d.MsgDelta
			m.TotalRunes += d.RuneDelta
			if d.LongestRunes > m.LongestRunes {
				m.LongestRunes = d.LongestRunes
				m.LongestUserID = d.LongestUserID
				m.LongestExcerpt = d.LongestExcerpt
				m.LongestFull = d.LongestFull
			}
			blob, err := json.Marshal(&m)
			if err != nil {
				return err
			}
			if err := rows.Put(dbKey, blob); err != nil {
				return err
			}
			continue
		}

		var s monthstats.MonthUserStat
		if existing := rows.Get(dbKey); existing != nil {
			if err := json.Unmarshal(existing, &s); err != nil {
				return err
			}
		} else {
			s.AbsChatID = key.AbsChatID
			s.Month = key.Month
			s.UserID = key.UserID
			s.FirstSeen = d.FirstSeen
		}
		s.MsgCount += d.MsgDelta
		s.RuneCount += d.RuneDelta
		s.CustomEmoji += d.CustomEmoji
		s.Code += d.Code
		s.Mention += d.Mention
		s.BotCommand += d.BotCommand
		s.KeywordCount += d.KeywordDelta
		if !d.FirstSeen.IsZero() && (s.FirstSeen.IsZero() || d.FirstSeen.Before(s.FirstSeen)) {
			s.FirstSeen = d.FirstSeen
		}
		blob, err := json.Marshal(&s)
		if err != nil {
			return err
		}
		if err := rows.Put(dbKey, blob); err != nil {
			return err
		}
	}
	return nil
}
