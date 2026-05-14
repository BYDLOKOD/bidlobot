package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/games/quiz"
)

var bktQuizLeaderboard = []byte("quiz_leaderboard")

// QuizRepo persists per-chat-per-user "first to solve" credit. The
// schema is:
//
//	ql:<absChatID:020d>:<userID:020d>  -> JSON{Entry}
//
// Sorting top-N is in-app: a 200-member chat with daily quiz play
// produces at most a few thousand entries, well below the threshold
// where a secondary index pays for itself.
type QuizRepo struct {
	db *bolt.DB
}

func NewQuizRepo(db *bolt.DB) *QuizRepo {
	return &QuizRepo{db: db}
}

// QuizKey returns the bbolt key for (absChatID, userID).
func QuizKey(absChatID, userID int64) []byte {
	return []byte(fmt.Sprintf("ql:%020d:%020d", absChatID, userID))
}

// QuizChatPrefix returns the scan prefix for all entries in a chat.
func QuizChatPrefix(absChatID int64) []byte {
	return []byte(fmt.Sprintf("ql:%020d:", absChatID))
}

// IncrementCorrect creates the entry on first call and bumps
// CorrectCount on subsequent calls. Username/FirstName are refreshed
// from e on every call so renames in Telegram propagate. Returns the
// updated entry through the bbolt write transaction.
func (r *QuizRepo) IncrementCorrect(_ context.Context, e quiz.Entry) error {
	if e.AbsChatID == 0 || e.UserID == 0 {
		return fmt.Errorf("quiz repo: zero AbsChatID or UserID")
	}
	return r.db.Update(func(tx *bolt.Tx) error {
		bkt := tx.Bucket(bktQuizLeaderboard)
		key := QuizKey(e.AbsChatID, e.UserID)

		var existing quiz.Entry
		if data := bkt.Get(key); data != nil {
			if err := json.Unmarshal(data, &existing); err != nil {
				return err
			}
		} else {
			existing = quiz.Entry{
				AbsChatID:    e.AbsChatID,
				UserID:       e.UserID,
				CorrectCount: 0,
			}
		}
		existing.CorrectCount++
		// Username/FirstName: overwrite so renames propagate. Empty
		// string in patch means "no update" - keep prior.
		if e.Username != "" {
			existing.Username = e.Username
		}
		if e.FirstName != "" {
			existing.FirstName = e.FirstName
		}
		if !e.LastPlayedAt.IsZero() {
			existing.LastPlayedAt = e.LastPlayedAt.UTC()
		}

		data, err := json.Marshal(&existing)
		if err != nil {
			return err
		}
		return bkt.Put(key, data)
	})
}

// GetEntry returns the leaderboard entry for (absChatID, userID), or
// quiz.ErrNotFound when the user has never solved a quiz in this chat.
func (r *QuizRepo) GetEntry(_ context.Context, absChatID, userID int64) (*quiz.Entry, error) {
	var e quiz.Entry
	err := r.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(bktQuizLeaderboard).Get(QuizKey(absChatID, userID))
		if data == nil {
			return quiz.ErrNotFound
		}
		return json.Unmarshal(data, &e)
	})
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// TopByChat returns up to limit entries from the chat sorted by
// CorrectCount desc, ties broken by older LastPlayedAt first (early
// solvers stay above newcomers with the same count). limit<=0 returns
// every entry.
func (r *QuizRepo) TopByChat(_ context.Context, absChatID int64, limit int) ([]quiz.Entry, error) {
	var all []quiz.Entry
	err := r.db.View(func(tx *bolt.Tx) error {
		bkt := tx.Bucket(bktQuizLeaderboard)
		c := bkt.Cursor()
		prefix := QuizChatPrefix(absChatID)
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			var e quiz.Entry
			if err := json.Unmarshal(v, &e); err != nil {
				continue
			}
			all = append(all, e)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].CorrectCount != all[j].CorrectCount {
			return all[i].CorrectCount > all[j].CorrectCount
		}
		return all[i].LastPlayedAt.Before(all[j].LastPlayedAt)
	})
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}
