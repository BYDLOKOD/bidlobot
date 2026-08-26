package storage

import (
	"context"
	"encoding/json"
	"fmt"

	bolt "go.etcd.io/bbolt"
)

var bktTikTokVideos = []byte("tiktok_videos")

// tiktokVideoRow is the value stored per (chat, video): the message id of
// the bot's repost of that video. Overwritten on every successful repost,
// so the row always points at the newest copy.
type tiktokVideoRow struct {
	MessageID int `json:"message_id"`
}

// TikTokVideoRepo records which TikTok videos the bot has reposted into
// which chat and where. The comment-quote pipeline consults it to reply
// to the video repost instead of posting a standalone quote.
type TikTokVideoRepo struct {
	db *bolt.DB
}

func NewTikTokVideoRepo(db *bolt.DB) *TikTokVideoRepo {
	return &TikTokVideoRepo{db: db}
}

// RecordVideo stores the message id of the bot's repost of videoID in
// chatID. A repeated repost of the same video overwrites the row.
func (r *TikTokVideoRepo) RecordVideo(_ context.Context, chatID int64, videoID string, msgID int) error {
	data, err := json.Marshal(tiktokVideoRow{MessageID: msgID})
	if err != nil {
		return fmt.Errorf("marshal tiktok video row: %w", err)
	}
	return r.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bktTikTokVideos).Put(TikTokVideoKey(chatID, videoID), data)
	})
}

// FindVideo returns the message id of the bot's repost of videoID in
// chatID, ok=false when no repost is recorded.
func (r *TikTokVideoRepo) FindVideo(_ context.Context, chatID int64, videoID string) (int, bool, error) {
	var msgID int
	found := false
	err := r.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bktTikTokVideos).Get(TikTokVideoKey(chatID, videoID))
		if v == nil {
			return nil
		}
		var row tiktokVideoRow
		if err := json.Unmarshal(v, &row); err != nil {
			return fmt.Errorf("unmarshal tiktok video row: %w", err)
		}
		msgID = row.MessageID
		found = true
		return nil
	})
	return msgID, found, err
}
