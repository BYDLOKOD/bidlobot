package storage

import (
	"context"
	"encoding/binary"
	"fmt"

	bolt "go.etcd.io/bbolt"
)

var bktAdmissionAttempts = []byte("admission_attempts")

type AdmissionAttemptRepo struct {
	db *bolt.DB
}

func NewAdmissionAttemptRepo(db *bolt.DB) *AdmissionAttemptRepo {
	return &AdmissionAttemptRepo{db: db}
}

// RecordUnauthorizedAdmission atomically increments the lifetime admission
// attempt count for one Telegram user and returns the new count.
func (r *AdmissionAttemptRepo) RecordUnauthorizedAdmission(_ context.Context, userID int64) (uint64, error) {
	if userID <= 0 {
		return 0, fmt.Errorf("record unauthorized admission: invalid user ID %d", userID)
	}

	var count uint64
	err := r.db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(bktAdmissionAttempts)
		if err != nil {
			return fmt.Errorf("create admission_attempts bucket: %w", err)
		}
		key := []byte(fmt.Sprintf("aa:%020d", userID))
		if value := bucket.Get(key); len(value) == 8 {
			count = binary.BigEndian.Uint64(value)
		}
		if count < ^uint64(0) {
			count++
		}
		value := make([]byte, 8)
		binary.BigEndian.PutUint64(value, count)
		return bucket.Put(key, value)
	})
	return count, err
}
