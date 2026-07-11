package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/domain/moderation"
	"github.com/veschin/bidlobot/internal/domain/monthstats"
	"github.com/veschin/bidlobot/internal/domain/stats"
)

// MigrationReport summarizes a chat-id rekey for diagnostics. All counters
// are best-effort: a partial migration still updates the report so the
// caller can decide whether to retry the operation.
type MigrationReport struct {
	OldAbsChatID      int64
	NewAbsChatID      int64
	StatsRekeyed      int
	StatsIndexes      int
	Members           int
	MemberIndex       int
	Chats             int
	Warnings          int
	WarnIndexes       int
	MonthStatsRekeyed int
	MonthStateMoved   int
	MonthSummaryMoved int
	MonthImportedIDs  int
	DailyStatsRekeyed int
}

// MigrateChatID rewrites every record keyed by oldAbs to be keyed by
// newAbs. The pending_actions and profiles* buckets are intentionally
// skipped (TTL'd / archived).
//
// Migration runs in a single bolt transaction so partial state cannot be
// observed by concurrent readers. If anything fails the transaction is
// rolled back and the bot can safely retry on the next 400-with-migrate
// response.
func MigrateChatID(_ context.Context, db *bolt.DB, oldAbs, newAbs int64) (*MigrationReport, error) {
	if oldAbs == 0 || newAbs == 0 {
		return nil, fmt.Errorf("migrate: invalid chat ids (old=%d new=%d)", oldAbs, newAbs)
	}
	if oldAbs == newAbs {
		return &MigrationReport{OldAbsChatID: oldAbs, NewAbsChatID: newAbs}, nil
	}

	report := &MigrationReport{OldAbsChatID: oldAbs, NewAbsChatID: newAbs}

	err := db.Update(func(tx *bolt.Tx) error {
		if err := migrateStats(tx, oldAbs, newAbs, report); err != nil {
			return fmt.Errorf("stats: %w", err)
		}
		if err := migrateMembers(tx, oldAbs, newAbs, report); err != nil {
			return fmt.Errorf("members: %w", err)
		}
		if err := migrateChats(tx, oldAbs, newAbs, report); err != nil {
			return fmt.Errorf("chats: %w", err)
		}
		if err := migrateWarnings(tx, oldAbs, newAbs, report); err != nil {
			return fmt.Errorf("warnings: %w", err)
		}
		if err := migrateMonthStats(tx, oldAbs, newAbs, report); err != nil {
			return fmt.Errorf("monthstats: %w", err)
		}
		if err := migrateMonthState(tx, oldAbs, newAbs); err != nil {
			return fmt.Errorf("monthstate: %w", err)
		}
		if err := migrateMonthSummary(tx, oldAbs, newAbs); err != nil {
			return fmt.Errorf("monthsummary: %w", err)
		}
		if err := migrateMonthImportedIDs(tx, oldAbs, newAbs); err != nil {
			return fmt.Errorf("monthimportedids: %w", err)
		}
		if err := migrateDailyStats(tx, oldAbs, newAbs); err != nil {
			return fmt.Errorf("dailystats: %w", err)
		}
		return nil
	})
	if err != nil {
		return report, err
	}
	return report, nil
}

// migrateStats rewrites the `stats` and `stats_by_chat` buckets. Each
// stats key embeds the chat id at the second position, and the value's
// ChatID field also stores the abs chat id.
func migrateStats(tx *bolt.Tx, oldAbs, newAbs int64, report *MigrationReport) error {
	statsBkt := tx.Bucket(bktStats)
	idxBkt := tx.Bucket(bktStatsByChat)

	// Walk the chat index first; it gives us the (absChatID, userID) pairs
	// we need without scanning the full stats bucket.
	prefix := StatsChatPrefix(oldAbs)
	type pair struct{ user int64 }
	users := make([]pair, 0)
	{
		c := idxBkt.Cursor()
		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			parts := bytes.SplitN(k, []byte(":"), 3)
			if len(parts) < 3 {
				continue
			}
			users = append(users, pair{user: parseID(parts[2])})
		}
	}

	for _, p := range users {
		oldKey := StatsKey(p.user, oldAbs)
		newKey := StatsKey(p.user, newAbs)

		data := statsBkt.Get(oldKey)
		if data == nil {
			// Stale index entry; clean up and skip.
			_ = idxBkt.Delete(StatsChatIndex(oldAbs, p.user))
			continue
		}
		var s stats.Stats
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("decode stats user=%d: %w", p.user, err)
		}
		s.ChatID = newAbs
		updated, err := json.Marshal(&s)
		if err != nil {
			return fmt.Errorf("encode stats user=%d: %w", p.user, err)
		}
		if err := statsBkt.Put(newKey, updated); err != nil {
			return err
		}
		if err := statsBkt.Delete(oldKey); err != nil {
			return err
		}
		if err := idxBkt.Put(StatsChatIndex(newAbs, p.user), nil); err != nil {
			return err
		}
		if err := idxBkt.Delete(StatsChatIndex(oldAbs, p.user)); err != nil {
			return err
		}
		report.StatsRekeyed++
		report.StatsIndexes++
	}
	return nil
}

func migrateMembers(tx *bolt.Tx, oldAbs, newAbs int64, report *MigrationReport) error {
	memBkt := tx.Bucket(bktMembers)
	idxBkt := tx.Bucket(bktMembersByChat)

	prefix := MemberChatPrefix(oldAbs)
	type pair struct{ user int64 }
	users := make([]pair, 0)
	{
		c := idxBkt.Cursor()
		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			tail := k[len(prefix):]
			users = append(users, pair{user: parseID(tail)})
		}
	}

	for _, p := range users {
		oldKey := MemberKey(p.user, oldAbs)
		newKey := MemberKey(p.user, newAbs)

		data := memBkt.Get(oldKey)
		if data == nil {
			_ = idxBkt.Delete(MemberChatIndex(oldAbs, p.user))
			continue
		}
		var m membership.Member
		if err := json.Unmarshal(data, &m); err != nil {
			return fmt.Errorf("decode member user=%d: %w", p.user, err)
		}
		m.AbsChatID = newAbs
		updated, err := json.Marshal(&m)
		if err != nil {
			return fmt.Errorf("encode member user=%d: %w", p.user, err)
		}
		if err := memBkt.Put(newKey, updated); err != nil {
			return err
		}
		if err := memBkt.Delete(oldKey); err != nil {
			return err
		}
		if err := idxBkt.Put(MemberChatIndex(newAbs, p.user), nil); err != nil {
			return err
		}
		if err := idxBkt.Delete(MemberChatIndex(oldAbs, p.user)); err != nil {
			return err
		}
		report.Members++
		report.MemberIndex++
	}
	return nil
}

func migrateChats(tx *bolt.Tx, oldAbs, newAbs int64, report *MigrationReport) error {
	bkt := tx.Bucket(bktChats)
	oldKey := ChatKey(oldAbs)
	data := bkt.Get(oldKey)
	if data == nil {
		return nil
	}
	var c membership.Chat
	if err := json.Unmarshal(data, &c); err != nil {
		return fmt.Errorf("decode chat: %w", err)
	}
	c.AbsChatID = newAbs

	// If a record already exists at the destination we keep its
	// InstalledAt timestamp (it represents an earlier observation).
	if existing := bkt.Get(ChatKey(newAbs)); existing != nil {
		var prev membership.Chat
		if err := json.Unmarshal(existing, &prev); err == nil {
			if !prev.InstalledAt.IsZero() && (c.InstalledAt.IsZero() || prev.InstalledAt.Before(c.InstalledAt)) {
				c.InstalledAt = prev.InstalledAt
			}
		}
	}
	updated, err := json.Marshal(&c)
	if err != nil {
		return fmt.Errorf("encode chat: %w", err)
	}
	if err := bkt.Put(ChatKey(newAbs), updated); err != nil {
		return err
	}
	if err := bkt.Delete(oldKey); err != nil {
		return err
	}
	report.Chats++
	return nil
}

// migrateWarnings updates both the warnings bucket (rewriting each
// matching warning's ChatID field in JSON) and the warns_by_target
// secondary index, where the chat id appears in the key prefix.
//
// We walk the secondary index (keyed by chat id) to collect uuids that
// need rewriting; this avoids a full scan of `warnings`.
func migrateWarnings(tx *bolt.Tx, oldAbs, newAbs int64, report *MigrationReport) error {
	warnBkt := tx.Bucket(bktWarnings)
	idxBkt := tx.Bucket(bktWarnsByTarget)

	type pair struct {
		target int64
		uuid   string
	}
	var pending []pair

	idxPrefix := []byte(fmt.Sprintf("wt:%020d:", oldAbs))
	c := idxBkt.Cursor()
	for k, _ := c.Seek(idxPrefix); k != nil && bytes.HasPrefix(k, idxPrefix); k, _ = c.Next() {
		// key shape: wt:absChatID:targetUserID:uuid
		// after prefix "wt:absChatID:" we have "targetUserID:uuid"
		tail := k[len(idxPrefix):]
		idx := bytes.IndexByte(tail, ':')
		if idx <= 0 {
			continue
		}
		target := parseID(tail[:idx])
		uuid := string(tail[idx+1:])
		pending = append(pending, pair{target: target, uuid: uuid})
	}

	for _, p := range pending {
		uid := p.uuid
		// Update the value: rewrite warning.ChatID.
		key := WarnKey(uid)
		data := warnBkt.Get(key)
		if data == nil {
			_ = idxBkt.Delete(WarnTargetIndex(oldAbs, p.target, uid))
			continue
		}
		var w moderation.Warning
		if err := json.Unmarshal(data, &w); err != nil {
			return fmt.Errorf("decode warning %s: %w", uid, err)
		}
		w.ChatID = newAbs
		updated, err := json.Marshal(&w)
		if err != nil {
			return fmt.Errorf("encode warning %s: %w", uid, err)
		}
		if err := warnBkt.Put(key, updated); err != nil {
			return err
		}

		// Move the index entry.
		if err := idxBkt.Put(WarnTargetIndex(newAbs, p.target, uid), nil); err != nil {
			return err
		}
		if err := idxBkt.Delete(WarnTargetIndex(oldAbs, p.target, uid)); err != nil {
			return err
		}
		report.Warnings++
		report.WarnIndexes++
	}
	return nil
}

// migrateMonthStats rewrites the stats_month bucket. Months are discovered
// via the month index (stats_month_idx). Each row's AbsChatID in the JSON
// value is updated. No index update needed since the index key also
// embeds the old chat id - the old index keys remain as stale orphans
// (they are value-less tombstones, harmless).
func migrateMonthStats(tx *bolt.Tx, oldAbs, newAbs int64, report *MigrationReport) error {
	monthBkt := tx.Bucket(bktMonth)
	idxBkt := tx.Bucket(bktMonthIdx)
	if monthBkt == nil || idxBkt == nil {
		return nil // nothing to migrate
	}

	// Walk the month index to discover which months this chat has data for.
	prefix := MonthStatsChatIndexPrefix(oldAbs)
	var months []string
	{
		c := idxBkt.Cursor()
		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			// key shape: msi:absChatID:YYYY-MM
			parts := bytes.SplitN(k, []byte(":"), 3)
			if len(parts) < 3 {
				continue
			}
			months = append(months, string(parts[2]))
		}
	}

	for _, month := range months {
		monthPrefix := MonthStatsMonthPrefix(oldAbs, month)
		c := monthBkt.Cursor()
		for k, v := c.Seek(monthPrefix); k != nil && bytes.HasPrefix(k, monthPrefix); k, v = c.Next() {
			// key shape: ms:absChatID:YYYY-MM:userID
			// Replace oldAbs with newAbs in the key.
			parts := bytes.SplitN(k, []byte(":"), 4)
			if len(parts) < 4 {
				continue
			}
			newKey := []byte(fmt.Sprintf("ms:%020d:%s:%s", newAbs, parts[2], parts[3]))

			// Classify by key: userID == MetaUserID (zero) is MonthMeta,
			// any other value is MonthUserStat.
			userID := parseID(parts[3])
			if userID == monthstats.MetaUserID {
				var meta monthstats.MonthMeta
				if err := json.Unmarshal(v, &meta); err != nil {
					return fmt.Errorf("decode monthmeta row %s: %w", k, err)
				}
				meta.AbsChatID = newAbs
				updated, err := json.Marshal(&meta)
				if err != nil {
					return fmt.Errorf("encode monthmeta row %s: %w", k, err)
				}
				if err := monthBkt.Put(newKey, updated); err != nil {
					return err
				}
			} else {
				var us monthstats.MonthUserStat
				if err := json.Unmarshal(v, &us); err != nil {
					return fmt.Errorf("decode monthuserstat row %s: %w", k, err)
				}
				us.AbsChatID = newAbs
				updated, err := json.Marshal(&us)
				if err != nil {
					return fmt.Errorf("encode monthuserstat row %s: %w", k, err)
				}
				if err := monthBkt.Put(newKey, updated); err != nil {
					return err
				}
			}
			if err := monthBkt.Delete(k); err != nil {
				return err
			}
			report.MonthStatsRekeyed++
		}

		// Rewrite the index key too.
		oldIdxKey := MonthStatsChatIndex(oldAbs, month)
		newIdxKey := MonthStatsChatIndex(newAbs, month)
		if idxBkt.Get(oldIdxKey) != nil {
			if err := idxBkt.Put(newIdxKey, nil); err != nil {
				return err
			}
			if err := idxBkt.Delete(oldIdxKey); err != nil {
				return err
			}
		}
	}
	return nil
}

// migrateMonthState rewrites the stats_month_state singleton.
func migrateMonthState(tx *bolt.Tx, oldAbs, newAbs int64) error {
	bkt := tx.Bucket(bktMonthState)
	if bkt == nil {
		return nil
	}

	oldKey := MonthStatsStateKey(oldAbs)
	data := bkt.Get(oldKey)
	if data == nil {
		return nil
	}

	var st monthstats.MonthState
	if err := json.Unmarshal(data, &st); err != nil {
		return fmt.Errorf("decode monthstate: %w", err)
	}
	st.AbsChatID = newAbs
	updated, err := json.Marshal(&st)
	if err != nil {
		return fmt.Errorf("encode monthstate: %w", err)
	}
	if err := bkt.Put(MonthStatsStateKey(newAbs), updated); err != nil {
		return err
	}
	if err := bkt.Delete(oldKey); err != nil {
		return err
	}
	return nil
}

// migrateMonthSummary rewrites the stats_month_summary bucket.
// The key format is msum:absChatID:YYYY-MM.
func migrateMonthSummary(tx *bolt.Tx, oldAbs, newAbs int64) error {
	bkt := tx.Bucket(bktMonthSummary)
	if bkt == nil {
		return nil
	}

	oldPrefix := []byte(fmt.Sprintf("msum:%020d:", oldAbs))
	c := bkt.Cursor()
	for k, v := c.Seek(oldPrefix); k != nil && bytes.HasPrefix(k, oldPrefix); k, v = c.Next() {
		// key shape: msum:absChatID:YYYY-MM
		parts := bytes.SplitN(k, []byte(":"), 3)
		if len(parts) < 3 {
			continue
		}
		newKey := []byte(fmt.Sprintf("msum:%020d:%s", newAbs, parts[2]))

		var s monthstats.MonthSummary
		if err := json.Unmarshal(v, &s); err != nil {
			return fmt.Errorf("decode monthsummary %s: %w", k, err)
		}
		s.AbsChatID = newAbs
		updated, err := json.Marshal(&s)
		if err != nil {
			return fmt.Errorf("encode monthsummary %s: %w", k, err)
		}
		if err := bkt.Put(newKey, updated); err != nil {
			return err
		}
		if err := bkt.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

// migrateMonthImportedIDs rewrites the stats_month_imported_ids bucket.
// The key format is mii:absChatID:messageID (value-less).
func migrateMonthImportedIDs(tx *bolt.Tx, oldAbs, newAbs int64) error {
	bkt := tx.Bucket(bktMonthImportedIDs)
	if bkt == nil {
		return nil
	}

	oldPrefix := MonthStatsImportedIDPrefix(oldAbs)
	c := bkt.Cursor()
	for k, _ := c.Seek(oldPrefix); k != nil && bytes.HasPrefix(k, oldPrefix); k, _ = c.Next() {
		// key shape: mii:absChatID:messageID
		parts := bytes.SplitN(k, []byte(":"), 3)
		if len(parts) < 3 {
			continue
		}
		newKey := []byte(fmt.Sprintf("mii:%020d:%s", newAbs, parts[2]))
		if err := bkt.Put(newKey, nil); err != nil {
			return err
		}
		if err := bkt.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

// migrateDailyStats rewrites the stats_daily bucket.
// The key format is sd:absChatID:YYYY-MM-DD:userID.
func migrateDailyStats(tx *bolt.Tx, oldAbs, newAbs int64) error {
	bkt := tx.Bucket(bktStatsDaily)
	if bkt == nil {
		return nil
	}

	oldPrefix := StatsDailyChatPrefix(oldAbs)
	c := bkt.Cursor()
	for k, v := c.Seek(oldPrefix); k != nil && bytes.HasPrefix(k, oldPrefix); k, v = c.Next() {
		// key shape: sd:absChatID:YYYY-MM-DD:userID
		parts := bytes.SplitN(k, []byte(":"), 4)
		if len(parts) < 4 {
			continue
		}
		newKey := []byte(fmt.Sprintf("sd:%020d:%s:%s", newAbs, parts[2], parts[3]))

		// Update the ChatID field in the value.
		var s stats.Stats
		if err := json.Unmarshal(v, &s); err != nil {
			return fmt.Errorf("decode dailystats row %s: %w", k, err)
		}
		s.ChatID = newAbs
		updated, err := json.Marshal(&s)
		if err != nil {
			return fmt.Errorf("encode dailystats row %s: %w", k, err)
		}
		if err := bkt.Put(newKey, updated); err != nil {
			return err
		}
		if err := bkt.Delete(k); err != nil {
			return err
		}
	}
	return nil
}
