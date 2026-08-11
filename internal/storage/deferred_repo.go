package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

var bktDeferred = []byte("deferred_jobs")

// Deferred job types.
const (
	DeferredTikTok    = "tiktok"
	DeferredSummarize = "summarize"
)

// DeferredTTL is the maximum lifetime of a queued job. Entries older
// than this are removed by GarbageCollect.
const DeferredTTL = 48 * time.Hour

// DeferredJob is a retry-able failed request. Each user has their own
// queue - /flush processes only the caller's jobs. Entries expire after
// DeferredTTL.
type DeferredJob struct {
	// Key is the bbolt bucket key. Set by ListByUser, ignored by Enqueue.
	Key string `json:"-"`

	UserID    int64           `json:"user_id"`
	Type      string          `json:"type"`
	ChatID    int64           `json:"chat_id"`
	MessageID int             `json:"message_id"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

// TikTokPayload is the type-specific data for a DeferredTikTok job.
type TikTokPayload struct {
	URL       string `json:"url"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	Caption   string `json:"caption"`
}

// SummarizePayload is the type-specific data for a DeferredSummarize job.
type SummarizePayload struct {
	N             int    `json:"n"`
	Questions     string `json:"questions"`
	PlaceholderID int    `json:"placeholder_id"`
	Requester     string `json:"requester"`
}

type DeferredRepo struct {
	db *bolt.DB
}

func NewDeferredRepo(db *bolt.DB) *DeferredRepo {
	return &DeferredRepo{db: db}
}

// Enqueue stores a job. The key is derived from CreatedAt (zero-padded
// UnixNano) so bbolt's lexicographic iteration yields FIFO order.
func (r *DeferredRepo) Enqueue(_ context.Context, job DeferredJob) error {
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now().UTC()
	}
	data, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("marshal deferred job: %w", err)
	}
	return r.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bktDeferred)
		ns := job.CreatedAt.UnixNano()
		key := fmt.Sprintf("dj:%020d", ns)
		for b.Get([]byte(key)) != nil {
			ns++
			key = fmt.Sprintf("dj:%020d", ns)
		}
		return b.Put([]byte(key), data)
	})
}

// ListByUser returns all jobs for the given user in chronological order.
func (r *DeferredRepo) ListByUser(_ context.Context, userID int64) ([]DeferredJob, error) {
	var jobs []DeferredJob
	err := r.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bktDeferred)
		return b.ForEach(func(k, v []byte) error {
			var job DeferredJob
			if err := json.Unmarshal(v, &job); err != nil {
				return fmt.Errorf("unmarshal deferred job: %w", err)
			}
			if job.UserID == userID {
				job.Key = string(k)
				jobs = append(jobs, job)
			}
			return nil
		})
	})
	return jobs, err
}

func (r *DeferredRepo) Delete(_ context.Context, key string) error {
	return r.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bktDeferred).Delete([]byte(key))
	})
}

// GarbageCollect removes entries whose CreatedAt is before the given
// cutoff. Returns the number of entries removed.
func (r *DeferredRepo) GarbageCollect(_ context.Context, before time.Time) (int, error) {
	before = before.UTC()
	removed := 0
	err := r.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bktDeferred)
		c := b.Cursor()
		var toDelete [][]byte
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var job DeferredJob
			if err := json.Unmarshal(v, &job); err != nil {
				continue
			}
			if job.CreatedAt.Before(before) {
				toDelete = append(toDelete, append([]byte(nil), k...))
			}
		}
		for _, k := range toDelete {
			if err := b.Delete(k); err != nil {
				return err
			}
			removed++
		}
		return nil
	})
	return removed, err
}
