package storage

import (
	"bytes"
	"encoding/json"

	bolt "go.etcd.io/bbolt"
)

// jsonBucket is a small generic wrapper over a single bbolt bucket of
// JSON-encoded rows. It exists to remove the repetitive
// View/Update + json.Marshal/Unmarshal boilerplate shared by the game
// repos. Rows are keyed by []byte keys the caller builds with the keyf
// helpers; values are plain JSON.
type jsonBucket[T any] struct {
	db   *bolt.DB
	name []byte
}

func newJSONBucket[T any](db *bolt.DB, name []byte) *jsonBucket[T] {
	return &jsonBucket[T]{db: db, name: name}
}

// Get returns the row for key, or notFound when the bucket or the key
// is missing.
func (b *jsonBucket[T]) Get(key []byte, notFound error) (*T, error) {
	var rec T
	err := b.db.View(func(tx *bolt.Tx) error {
		bkt := tx.Bucket(b.name)
		if bkt == nil {
			return notFound
		}
		data := bkt.Get(key)
		if data == nil {
			return notFound
		}
		return json.Unmarshal(data, &rec)
	})
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// Put writes the row unconditionally, creating the bucket on first use.
func (b *jsonBucket[T]) Put(key []byte, rec T) error {
	data, err := json.Marshal(&rec)
	if err != nil {
		return err
	}
	return b.db.Update(func(tx *bolt.Tx) error {
		bkt, err := tx.CreateBucketIfNotExists(b.name)
		if err != nil {
			return err
		}
		return bkt.Put(key, data)
	})
}

// Delete removes the row; a missing bucket or row is a no-op.
func (b *jsonBucket[T]) Delete(key []byte) error {
	return b.db.Update(func(tx *bolt.Tx) error {
		bkt := tx.Bucket(b.name)
		if bkt == nil {
			return nil
		}
		return bkt.Delete(key)
	})
}

// Update reads the row (zero value when absent), lets mutate adjust it
// in place, and writes it back, all inside one write transaction. The
// bucket is created on first use. This is the read-modify-write shape
// behind the repos' increment methods.
func (b *jsonBucket[T]) Update(key []byte, mutate func(rec *T) error) error {
	return b.db.Update(func(tx *bolt.Tx) error {
		bkt, err := tx.CreateBucketIfNotExists(b.name)
		if err != nil {
			return err
		}
		var rec T
		if data := bkt.Get(key); data != nil {
			if err := json.Unmarshal(data, &rec); err != nil {
				return err
			}
		}
		if err := mutate(&rec); err != nil {
			return err
		}
		data, err := json.Marshal(&rec)
		if err != nil {
			return err
		}
		return bkt.Put(key, data)
	})
}

// ScanPrefix calls fn for every (key, value) under prefix in key order,
// skipping rows that fail to unmarshal. A missing bucket yields no rows.
func (b *jsonBucket[T]) ScanPrefix(prefix []byte, fn func(key []byte, rec T) error) error {
	return b.db.View(func(tx *bolt.Tx) error {
		bkt := tx.Bucket(b.name)
		if bkt == nil {
			return nil
		}
		c := bkt.Cursor()
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			var rec T
			if err := json.Unmarshal(v, &rec); err != nil {
				continue
			}
			if err := fn(k, rec); err != nil {
				return err
			}
		}
		return nil
	})
}
