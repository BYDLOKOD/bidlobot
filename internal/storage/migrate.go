package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/domain/membership"
	"github.com/veschin/bidlobot/internal/domain/moderation"
	"github.com/veschin/bidlobot/internal/domain/stats"
)

// MigrationReport summarizes a chat-id rekey for diagnostics. All counters
// are best-effort: a partial migration still updates the report so the
// caller can decide whether to retry the operation.
type MigrationReport struct {
	OldAbsChatID int64
	NewAbsChatID int64
	StatsRekeyed int
	StatsIndexes int
	Members      int
	MemberIndex  int
	Chats        int
	Warnings     int
	WarnIndexes  int
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
