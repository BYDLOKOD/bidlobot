package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/domain/membership"
)

var (
	bktMembers       = []byte("members")
	bktMembersByChat = []byte("members_by_chat")
	bktChats         = []byte("chats")
)

type MembershipRepo struct {
	db *bolt.DB
}

func NewMembershipRepo(db *bolt.DB) *MembershipRepo {
	return &MembershipRepo{db: db}
}

func nowOr(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t.UTC()
}

func laterOf(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func earlierNonZero(a, b time.Time) time.Time {
	if a.IsZero() {
		return b
	}
	if b.IsZero() {
		return a
	}
	if a.Before(b) {
		return a
	}
	return b
}

// UpsertMember atomically reads the existing member record, applies the
// patch, and writes it back. New members are created on first observation.
// Counters increment additively, timestamps move only forward, optional
// scalar fields are overwritten only when the patch sets them.
func (r *MembershipRepo) UpsertMember(_ context.Context, p membership.MemberPatch) (*membership.Member, error) {
	if p.UserID == 0 || p.AbsChatID == 0 {
		return nil, membership.ErrNotFound
	}
	now := nowOr(p.Now)

	var result membership.Member
	err := r.db.Update(func(tx *bolt.Tx) error {
		mem := tx.Bucket(bktMembers)
		idx := tx.Bucket(bktMembersByChat)
		key := MemberKey(p.UserID, p.AbsChatID)

		var m membership.Member
		if data := mem.Get(key); data != nil {
			if err := json.Unmarshal(data, &m); err != nil {
				return err
			}
		} else {
			m = membership.Member{
				UserID:      p.UserID,
				AbsChatID:   p.AbsChatID,
				FirstSeenAt: now,
			}
			if err := idx.Put(MemberChatIndex(p.AbsChatID, p.UserID), nil); err != nil {
				return err
			}
		}

		if p.Username != nil {
			m.Username = strings.ToLower(strings.TrimSpace(*p.Username))
		}
		if p.FirstName != nil {
			m.FirstName = *p.FirstName
		}
		if p.IsBot != nil {
			m.IsBot = *p.IsBot
		}
		if p.IsPremium != nil {
			m.IsPremium = *p.IsPremium
		}
		if p.Status != membership.StatusUnknown {
			m.Status = p.Status
		}
		if p.KnownVia != "" {
			m.KnownVia = p.KnownVia
		}
		m.JoinedAt = earlierNonZero(m.JoinedAt, p.JoinedAt)
		if !p.LeftAt.IsZero() {
			m.LeftAt = laterOf(m.LeftAt, p.LeftAt)
		}
		if !p.LastMessageAt.IsZero() {
			m.LastMessageAt = laterOf(m.LastMessageAt, p.LastMessageAt)
		}
		if !p.LastReactionAt.IsZero() {
			m.LastReactionAt = laterOf(m.LastReactionAt, p.LastReactionAt)
		}
		m.LastSeenAt = laterOf(m.LastSeenAt, now)
		m.MessageCount += p.IncMessageCount
		m.ReactionCount += p.IncReactionCount
		// Absolute set from a bulk import: max() so re-import is
		// idempotent and a realtime count accumulated since deploy is
		// never reduced. Inc* and Set* are never combined in one patch.
		if p.SetMessageCount != nil {
			m.MessageCount = max(m.MessageCount, *p.SetMessageCount)
		}
		if p.SetReactionCount != nil {
			m.ReactionCount = max(m.ReactionCount, *p.SetReactionCount)
		}
		m.UpdatedAt = now

		data, err := json.Marshal(&m)
		if err != nil {
			return err
		}
		if err := mem.Put(key, data); err != nil {
			return err
		}
		result = m
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *MembershipRepo) GetMember(_ context.Context, userID, absChatID int64) (*membership.Member, error) {
	var m membership.Member
	err := r.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(bktMembers).Get(MemberKey(userID, absChatID))
		if data == nil {
			return membership.ErrNotFound
		}
		return json.Unmarshal(data, &m)
	})
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// GetMemberByUsername scans the chat's secondary index. O(N) over chat
// members but acceptable: for a 200-member chat this is a few-hundred-µs
// scan, and it is invoked only for explicit @username addressing.
func (r *MembershipRepo) GetMemberByUsername(_ context.Context, absChatID int64, username string) (*membership.Member, error) {
	wanted := strings.ToLower(strings.TrimSpace(username))
	if wanted == "" {
		return nil, membership.ErrNotFound
	}
	var found *membership.Member

	err := r.db.View(func(tx *bolt.Tx) error {
		mem := tx.Bucket(bktMembers)
		idx := tx.Bucket(bktMembersByChat)
		prefix := MemberChatPrefix(absChatID)

		c := idx.Cursor()
		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			tail := k[len(prefix):]
			userID := parseID(tail)
			if userID == 0 {
				continue
			}
			data := mem.Get(MemberKey(userID, absChatID))
			if data == nil {
				continue
			}
			var m membership.Member
			if err := json.Unmarshal(data, &m); err != nil {
				continue
			}
			if m.Username == wanted {
				found = &m
				return nil
			}
		}
		return membership.ErrNotFound
	})
	if err != nil && found == nil {
		return nil, err
	}
	return found, nil
}

func (r *MembershipRepo) ListByChat(_ context.Context, absChatID int64) ([]membership.Member, error) {
	var results []membership.Member
	err := r.db.View(func(tx *bolt.Tx) error {
		mem := tx.Bucket(bktMembers)
		idx := tx.Bucket(bktMembersByChat)
		prefix := MemberChatPrefix(absChatID)

		c := idx.Cursor()
		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			tail := k[len(prefix):]
			userID := parseID(tail)
			if userID == 0 {
				continue
			}
			data := mem.Get(MemberKey(userID, absChatID))
			if data == nil {
				continue
			}
			var m membership.Member
			if err := json.Unmarshal(data, &m); err != nil {
				continue
			}
			results = append(results, m)
		}
		return nil
	})
	return results, err
}

func (r *MembershipRepo) UpsertChat(_ context.Context, c membership.Chat) error {
	if c.AbsChatID == 0 {
		return membership.ErrChatNotFound
	}
	if c.LastUpdateAt.IsZero() {
		c.LastUpdateAt = time.Now().UTC()
	}
	return r.db.Update(func(tx *bolt.Tx) error {
		bkt := tx.Bucket(bktChats)
		key := ChatKey(c.AbsChatID)

		if existing := bkt.Get(key); existing != nil {
			var prev membership.Chat
			if err := json.Unmarshal(existing, &prev); err == nil {
				if c.InstalledAt.IsZero() {
					c.InstalledAt = prev.InstalledAt
				}
			}
		} else if c.InstalledAt.IsZero() {
			c.InstalledAt = c.LastUpdateAt
		}

		data, err := json.Marshal(&c)
		if err != nil {
			return err
		}
		return bkt.Put(key, data)
	})
}

func (r *MembershipRepo) GetChat(_ context.Context, absChatID int64) (*membership.Chat, error) {
	var c membership.Chat
	err := r.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(bktChats).Get(ChatKey(absChatID))
		if data == nil {
			return membership.ErrChatNotFound
		}
		return json.Unmarshal(data, &c)
	})
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *MembershipRepo) ListChats(_ context.Context) ([]membership.Chat, error) {
	var results []membership.Chat
	err := r.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bktChats).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var ch membership.Chat
			if err := json.Unmarshal(v, &ch); err != nil {
				continue
			}
			results = append(results, ch)
		}
		return nil
	})
	return results, err
}
