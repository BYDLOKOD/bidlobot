package storage

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	GameStateBucket    = "game_v1"
	GameDeadlineBucket = "game_deadlines_v1"
	GameRetiredBucket  = "game_retired_chats_v1"
	GameRenderBucket   = "game_render_v1"
)

type GameAction struct {
	ChatID, UserID int64
	CardID         int
	Eligible       []int64
	Key, Verb, Arg string
	Now            time.Time
}

type GameDeadline struct {
	Module string
	Action GameAction
}

func migrateGameChat(tx *bolt.Tx, from, to int64) error {
	oldKey := binary.BigEndian.AppendUint64(nil, uint64(from))
	newKey := binary.BigEndian.AppendUint64(nil, uint64(to))
	if root := tx.Bucket([]byte(GameStateBucket)); root != nil {
		if source := root.Bucket(oldKey); source != nil {
			if root.Bucket(newKey) != nil {
				return fmt.Errorf("game migration: target chat already has state")
			}
			destination, err := root.CreateBucket(newKey)
			if err != nil {
				return err
			}
			if err := copyGameBucket(source, destination); err != nil {
				return err
			}
			if err := root.DeleteBucket(oldKey); err != nil {
				return err
			}
		}
	}
	if b := tx.Bucket([]byte(GameRenderBucket)); b != nil {
		if value := b.Get(oldKey); value != nil {
			if b.Get(newKey) != nil {
				return fmt.Errorf("game migration: delivery collision")
			}
			if err := b.Put(newKey, value); err != nil {
				return err
			}
			if err := b.Delete(oldKey); err != nil {
				return err
			}
		}
	}
	if b := tx.Bucket([]byte(GameDeadlineBucket)); b != nil {
		type timer struct {
			Old   []byte
			New   []byte
			Value []byte
		}
		var timers []timer
		if err := b.ForEach(func(k, v []byte) error {
			if len(k) < 16 {
				return fmt.Errorf("game migration: invalid deadline key")
			}
			if binary.BigEndian.Uint64(k[8:16]) != uint64(from) {
				return nil
			}
			var d GameDeadline
			if err := json.Unmarshal(v, &d); err != nil {
				return err
			}
			d.Action.ChatID = to
			value, err := json.Marshal(d)
			if err != nil {
				return err
			}
			key := append([]byte(nil), k...)
			copy(key[8:16], newKey)
			timers = append(timers, timer{append([]byte(nil), k...), key, value})
			return nil
		}); err != nil {
			return err
		}
		for _, t := range timers {
			if b.Get(t.New) != nil {
				return fmt.Errorf("game migration: deadline collision")
			}
			if err := b.Put(t.New, t.Value); err != nil {
				return err
			}
			if err := b.Delete(t.Old); err != nil {
				return err
			}
		}
	}
	retired, err := tx.CreateBucketIfNotExists([]byte(GameRetiredBucket))
	if err != nil {
		return err
	}
	return retired.Put(oldKey, newKey)
}

func copyGameBucket(src, dst *bolt.Bucket) error {
	if err := dst.SetSequence(src.Sequence()); err != nil {
		return err
	}
	return src.ForEach(func(k, v []byte) error {
		if v != nil {
			return dst.Put(k, v)
		}
		b, err := dst.CreateBucket(k)
		if err != nil {
			return err
		}
		return copyGameBucket(src.Bucket(k), b)
	})
}
