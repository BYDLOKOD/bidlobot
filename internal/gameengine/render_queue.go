package gameengine

import (
	"encoding/binary"
	"encoding/json"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/storage"
)

type RenderIntent struct {
	ChatID   int64
	CardID   int
	Revision uint64
	Result   Result
}

var cardKey = []byte("prod_card")

func queueRender(tx *bolt.Tx, a Action, r Result) error {
	if r.Text == "" {
		return nil
	}
	b, err := State(tx, a.ChatID, "_presentation")
	if err != nil {
		return err
	}
	if a.Verb == "open" && a.CardID != 0 {
		if err := b.Put(cardKey, number(int64(a.CardID))); err != nil {
			return err
		}
	}
	card := b.Get(cardKey)
	if len(card) != 8 {
		return nil
	}
	q, err := tx.CreateBucketIfNotExists([]byte(storage.GameRenderBucket))
	if err != nil {
		return err
	}
	seq, err := q.NextSequence()
	if err != nil {
		return err
	}
	value, err := json.Marshal(RenderIntent{CardID: int(binary.BigEndian.Uint64(card)), Revision: seq, Result: r})
	if err != nil {
		return err
	}
	return q.Put(number(a.ChatID), value)
}

func (e *Engine) PendingRenders() ([]RenderIntent, error) {
	var result []RenderIntent
	err := e.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(storage.GameRenderBucket))
		if b == nil {
			return nil
		}
		c := b.Cursor()
		for k, v := c.First(); k != nil && len(result) < 32; k, v = c.Next() {
			var r RenderIntent
			if err := json.Unmarshal(v, &r); err != nil {
				return err
			}
			r.ChatID = int64(binary.BigEndian.Uint64(k))
			result = append(result, r)
		}
		return nil
	})
	return result, err
}

func (e *Engine) AcknowledgeRender(intent RenderIntent) error {
	return e.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(storage.GameRenderBucket))
		if b == nil {
			return nil
		}
		k := number(intent.ChatID)
		v := b.Get(k)
		if v == nil {
			return nil
		}
		var latest RenderIntent
		if err := json.Unmarshal(v, &latest); err != nil {
			return err
		}
		if latest.Revision != intent.Revision {
			return nil
		}
		return b.Delete(k)
	})
}
