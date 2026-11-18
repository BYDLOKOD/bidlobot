package automat

import (
	"encoding/binary"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/gameengine"
)

// PendingReply is a durable reservation for one automatic reply. The caller
// must re-check the target's live state with DeliveryPhrase immediately
// before sending and decline the send when ok is false.
type PendingReply struct {
	OwnerID         int64
	SourceMessageID int64
	PhraseID        int
	Phrase          string
}

// Settings is a read view of one member's automat state in one chat.
type Settings struct {
	Owned    []Item
	Equipped Item // ID == 0 when nothing is equipped
	Enabled  bool
}

// Lookup returns the member's collection and automatic reply state without
// modifying it. It is safe inside a read-only transaction.
func Lookup(tx *bolt.Tx, chatID, userID int64) (Settings, error) {
	if chatID <= 0 || userID <= 0 {
		return Settings{}, fmt.Errorf("automat: invalid chat or user id")
	}
	st, err := gameengine.State(tx, chatID, ModuleName)
	if err != nil {
		return Settings{}, err
	}
	var out Settings
	if st == nil {
		return out, nil
	}
	p, err := loadPlayer(st, userID)
	if err != nil {
		return Settings{}, err
	}
	for _, it := range catalogue {
		if p.mask&(1<<uint(it.ID-1)) != 0 {
			out.Owned = append(out.Owned, it)
		}
	}
	if it, ok := itemByID(p.equipped); ok {
		out.Equipped = it
	}
	out.Enabled = p.enabled
	return out, nil
}

// ReserveReply atomically reserves at most one automatic reply for the tagged
// member identified by the parent from Telegram mention entities.
//
// It returns nil without an error when no reply is due: the member has no
// equipped phrase, automatic replies are disabled, the original message was
// already handled, or a cooldown is active. The parent owns entity parsing,
// membership checks, bot/anonymous filtering and the actual send.
func ReserveReply(tx *bolt.Tx, chatID, ownerID, sourceMessageID int64, now time.Time) (*PendingReply, error) {
	if tx == nil || !tx.Writable() {
		return nil, fmt.Errorf("automat: ReserveReply requires a writable transaction")
	}
	if chatID <= 0 || ownerID <= 0 || sourceMessageID <= 0 {
		return nil, fmt.Errorf("automat: invalid reservation ids")
	}
	if now.IsZero() {
		return nil, fmt.Errorf("automat: reservation time is zero")
	}
	st, err := gameengine.State(tx, chatID, ModuleName)
	if err != nil {
		return nil, err
	}
	if st == nil {
		return nil, fmt.Errorf("automat: state bucket unavailable")
	}
	dedup, err := st.CreateBucketIfNotExists([]byte(dedupBucket))
	if err != nil {
		return nil, err
	}
	msgKey := messageKey(sourceMessageID)
	if dedup.Get(msgKey) != nil {
		return nil, nil
	}
	p, err := loadPlayer(st, ownerID)
	if err != nil {
		return nil, err
	}
	if !p.enabled || p.equipped == 0 {
		return nil, nil
	}
	item, ok := itemByID(p.equipped)
	if !ok {
		return nil, nil
	}
	blocked, err := cooldownActive(st, chatCoolBkt, []byte(chatCoolKey), now, ChatCooldown)
	if err != nil {
		return nil, err
	}
	if blocked {
		return nil, nil
	}
	blocked, err = cooldownActive(st, targetCoolBkt, userKey(ownerID), now, TargetCooldown)
	if err != nil {
		return nil, err
	}
	if blocked {
		return nil, nil
	}

	if err := stampCooldown(st, chatCoolBkt, []byte(chatCoolKey), now); err != nil {
		return nil, err
	}
	if err := stampCooldown(st, targetCoolBkt, userKey(ownerID), now); err != nil {
		return nil, err
	}
	var rec [9]byte
	binary.BigEndian.PutUint64(rec[0:8], uint64(ownerID))
	rec[8] = byte(item.ID)
	if err := dedup.Put(msgKey, rec[:]); err != nil {
		return nil, err
	}
	return &PendingReply{
		OwnerID:         ownerID,
		SourceMessageID: sourceMessageID,
		PhraseID:        item.ID,
		Phrase:          item.Phrase,
	}, nil
}

// DeliveryPhrase returns the phrase the member has equipped right now and
// whether automatic replies are currently enabled. The parent calls it
// immediately before sending a reserved reply, after its own membership
// check, because equipping, disabling and leaving are all rechecked at
// delivery time.
func DeliveryPhrase(tx *bolt.Tx, chatID, ownerID int64) (string, bool, error) {
	if chatID <= 0 || ownerID <= 0 {
		return "", false, fmt.Errorf("automat: invalid chat or user id")
	}
	st, err := gameengine.State(tx, chatID, ModuleName)
	if err != nil {
		return "", false, err
	}
	if st == nil {
		return "", false, nil
	}
	p, err := loadPlayer(st, ownerID)
	if err != nil {
		return "", false, err
	}
	if !p.enabled || p.equipped == 0 {
		return "", false, nil
	}
	item, ok := itemByID(p.equipped)
	if !ok || p.mask&(1<<uint(item.ID-1)) == 0 {
		return "", false, nil
	}
	return item.Phrase, true, nil
}

func messageKey(id int64) []byte {
	var k [8]byte
	binary.BigEndian.PutUint64(k[:], uint64(id))
	return k[:]
}

func cooldownActive(st *bolt.Bucket, bucket string, key []byte, now time.Time, window time.Duration) (bool, error) {
	cb := st.Bucket([]byte(bucket))
	if cb == nil {
		return false, nil
	}
	raw := cb.Get(key)
	if len(raw) != 8 {
		return false, nil
	}
	last := time.Unix(0, int64(binary.BigEndian.Uint64(raw))).UTC()
	if now.Sub(last) < window {
		return true, nil
	}
	return false, nil
}

func stampCooldown(st *bolt.Bucket, bucket string, key []byte, now time.Time) error {
	cb, err := st.CreateBucketIfNotExists([]byte(bucket))
	if err != nil {
		return err
	}
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], uint64(now.UnixNano()))
	return cb.Put(key, raw[:])
}
