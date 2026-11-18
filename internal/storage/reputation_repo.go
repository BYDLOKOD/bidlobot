package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/domain/reputation"
)

// Bucket name owned by the reputation domain. It is created on first
// write via CreateBucketIfNotExists so the repo works even before bolt.go
// registers it (and stays correct after it does).
var bktReputation = []byte("reputation")

const (
	// repVersion is the on-disk wallet shape: available, reserved and
	// score in one record. Version 1 was a single balance value.
	repVersion = 2

	// repDefaultAvailable is the new-member wallet. The title score
	// starts at zero regardless of Telegram administrator status.
	repDefaultAvailable = 10

	// repMaxOutgoingPerDay is the sender limit shared by slash and
	// reaction settlements.
	repMaxOutgoingPerDay = 3

	repDayFormat = "2006-01-02"
)

var (
	// repVersionKey is the committed migration marker. Its presence means
	// legacy {balance} records have already been seeded.
	repVersionKey = []byte("meta:v")

	repAccountPrefix = []byte("r:")
)

// repMarker is the non-nil value of a presence-only key. bbolt's Get
// cannot distinguish a missing key from one stored with a nil value, so
// pair and source markers must carry at least one byte.
var repMarker = []byte{1}

// accountRecord is the on-disk JSON shape. The legacy v1 record had only
// "balance"; the pointer fields distinguish a missing value from zero.
type accountRecord struct {
	Available *int `json:"available,omitempty"`
	Reserved  *int `json:"reserved,omitempty"`
	Score     *int `json:"score,omitempty"`
	Balance   *int `json:"balance,omitempty"`
}

// accountJSON is the write shape: new fields only, never the legacy
// balance field.
type accountJSON struct {
	Available int `json:"available"`
	Reserved  int `json:"reserved"`
	Score     int `json:"score"`
}

type countRecord struct {
	Count int `json:"count"`
}

func defaultAccount(chatID, userID int64) reputation.Account {
	return reputation.Account{ChatID: chatID, UserID: userID, Available: repDefaultAvailable}
}

// repKey returns the bbolt key for (absChatID, userID).
// Examples: "r:00000000000000000100:00000000000000000001"
func repKey(absChatID, userID int64) []byte {
	return keyf("r:%020d:%020d", absChatID, userID)
}

// repChatPrefix returns the prefix to scan all users in a chat.
func repChatPrefix(absChatID int64) []byte {
	return keyf("r:%020d:", absChatID)
}

func repPairKey(absChatID int64, day string, a, b int64) []byte {
	if a > b {
		a, b = b, a
	}
	return keyf("rp:%020d:%s:%020d:%020d", absChatID, day, a, b)
}

func repPairPrefix(absChatID int64) []byte { return keyf("rp:%020d:", absChatID) }

func repSenderKey(absChatID int64, day string, userID int64) []byte {
	return keyf("ro:%020d:%s:%020d", absChatID, day, userID)
}

func repSenderPrefix(absChatID int64) []byte { return keyf("ro:%020d:", absChatID) }

func repSourceKey(absChatID, actorID int64, source reputation.SourceKind, messageID int) []byte {
	return keyf("rs:%020d:%020d:%s:%d", absChatID, actorID, source, messageID)
}

func repSourcePrefix(absChatID int64) []byte { return keyf("rs:%020d:", absChatID) }

func repDayMetaKey(absChatID int64) []byte { return keyf("rd:%020d", absChatID) }

// parseUserIDFromRepKey extracts the userID from a repKey.
func parseUserIDFromRepKey(key []byte) int64 {
	// Key format: r:CHAT:USER  (split on ':')
	parts := bytes.Split(key, []byte(":"))
	if len(parts) < 3 {
		return 0
	}
	return parseID(parts[2])
}

// decodeAccount parses either the current record or the legacy v1 one.
// legacy reports that only a balance was stored; ok is false for a
// corrupt row, which callers treat as absent.
func decodeAccount(data []byte, chatID, userID int64) (account reputation.Account, legacy bool, ok bool) {
	var rec accountRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return defaultAccount(chatID, userID), false, false
	}
	account = defaultAccount(chatID, userID)
	legacy = rec.Available == nil && rec.Reserved == nil && rec.Score == nil && rec.Balance != nil
	switch {
	case rec.Available != nil:
		account.Available = *rec.Available
	case rec.Balance != nil:
		account.Available = *rec.Balance
	}
	if rec.Reserved != nil {
		account.Reserved = *rec.Reserved
	}
	switch {
	case rec.Score != nil:
		account.Score = *rec.Score
	case rec.Balance != nil:
		// Seed the title score from the legacy balance once, without
		// inventing activity history.
		account.Score = max(0, *rec.Balance)
	}
	if account.Available < 0 {
		account.Available = 0
	}
	if account.Reserved < 0 {
		account.Reserved = 0
	}
	if account.Score < 0 {
		account.Score = 0
	}
	return account, legacy, true
}

func writeAccount(bkt *bolt.Bucket, account reputation.Account) error {
	data, err := json.Marshal(accountJSON{
		Available: account.Available,
		Reserved:  account.Reserved,
		Score:     account.Score,
	})
	if err != nil {
		return err
	}
	return bkt.Put(repKey(account.ChatID, account.UserID), data)
}

// readAccount reads the persisted account, or the defaults when the row
// is absent or corrupt.
func readAccount(bkt *bolt.Bucket, chatID, userID int64) reputation.Account {
	if bkt == nil {
		return defaultAccount(chatID, userID)
	}
	data := bkt.Get(repKey(chatID, userID))
	if data == nil {
		return defaultAccount(chatID, userID)
	}
	account, _, ok := decodeAccount(data, chatID, userID)
	if !ok {
		return defaultAccount(chatID, userID)
	}
	return account
}

// ensureMigrated seeds every legacy balance into available and score in
// one transaction and commits the version marker. Returns the number of
// rows rewritten. It is a no-op once the marker is present.
func ensureMigrated(bkt *bolt.Bucket) (int, error) {
	if v := bkt.Get(repVersionKey); v != nil {
		if n, err := strconv.Atoi(string(v)); err == nil && n >= repVersion {
			return 0, nil
		}
	}

	var keys [][]byte
	c := bkt.Cursor()
	for k, _ := c.Seek(repAccountPrefix); k != nil && bytes.HasPrefix(k, repAccountPrefix); k, _ = c.Next() {
		keys = append(keys, append([]byte(nil), k...))
	}

	migrated := 0
	for _, key := range keys {
		data := bkt.Get(key)
		if data == nil {
			continue
		}
		userID := parseUserIDFromRepKey(key)
		account, legacy, ok := decodeAccount(data, 0, userID)
		if !ok {
			continue
		}
		// Rows that already carry the new shape are left alone, so a
		// repeated migration cannot double a balance.
		var rec accountRecord
		if err := json.Unmarshal(data, &rec); err == nil && rec.Available != nil {
			continue
		}
		if !legacy {
			continue
		}
		account.ChatID = parseChatIDFromRepKey(key)
		if err := writeAccount(bkt, account); err != nil {
			return migrated, err
		}
		migrated++
	}
	if err := bkt.Put(repVersionKey, []byte(strconv.Itoa(repVersion))); err != nil {
		return migrated, err
	}
	return migrated, nil
}

// parseChatIDFromRepKey extracts the chat id from r:<chat>:<user>.
func parseChatIDFromRepKey(key []byte) int64 {
	parts := bytes.Split(key, []byte(":"))
	if len(parts) < 3 {
		return 0
	}
	return parseID(parts[1])
}

// MigrateReputation seeds the two-value wallet from every legacy
// balance exactly once. backup, when non-nil, receives a consistent
// snapshot of the database before the migration transaction starts.
// Returns the number of rewritten rows.
func MigrateReputation(db *bolt.DB, backup io.Writer) (int, error) {
	if db == nil {
		return 0, fmt.Errorf("reputation migration: nil database")
	}
	if backup != nil {
		if err := db.View(func(tx *bolt.Tx) error {
			_, err := tx.WriteTo(backup)
			return err
		}); err != nil {
			return 0, fmt.Errorf("reputation migration backup: %w", err)
		}
	}
	var migrated int
	err := db.Update(func(tx *bolt.Tx) error {
		bkt, err := tx.CreateBucketIfNotExists(bktReputation)
		if err != nil {
			return fmt.Errorf("create reputation bucket: %w", err)
		}
		migrated, err = ensureMigrated(bkt)
		return err
	})
	if err != nil {
		return migrated, err
	}
	return migrated, nil
}

// ReputationMigrationNeeded reports whether legacy v1 balance rows still
// have to be seeded. It is read-only, so startup can decide to take a
// backup before calling MigrateReputation without creating one on every
// boot.
func ReputationMigrationNeeded(db *bolt.DB) (bool, error) {
	if db == nil {
		return false, fmt.Errorf("reputation migration check: nil database")
	}
	needed := false
	err := db.View(func(tx *bolt.Tx) error {
		bkt := tx.Bucket(bktReputation)
		if bkt == nil {
			return nil
		}
		if v := bkt.Get(repVersionKey); v != nil {
			if n, err := strconv.Atoi(string(v)); err == nil && n >= repVersion {
				return nil
			}
		}
		c := bkt.Cursor()
		for k, v := c.Seek(repAccountPrefix); k != nil && bytes.HasPrefix(k, repAccountPrefix); k, v = c.Next() {
			userID := parseUserIDFromRepKey(k)
			_, legacy, ok := decodeAccount(v, 0, userID)
			if ok && legacy {
				needed = true
				return nil
			}
		}
		return nil
	})
	return needed, err
}

// ReputationWallet is the shared wallet read used inside another
// domain's bbolt transaction. It normalizes the chat id, returns the
// new-member defaults for an absent record, and never writes.
func ReputationWallet(tx *bolt.Tx, chatID, userID int64) (reputation.Account, error) {
	if tx == nil {
		return reputation.Account{}, fmt.Errorf("reputation wallet: nil transaction")
	}
	absChatID := AbsChatID(chatID)
	return readAccount(tx.Bucket(bktReputation), absChatID, userID), nil
}

// AdjustReputationWallet applies signed wallet deltas inside the
// caller's transaction. Available cannot fall below zero; reserved
// cannot fall below zero. The title score is never touched.
func AdjustReputationWallet(tx *bolt.Tx, chatID, userID int64, available, reserved int) error {
	if tx == nil {
		return fmt.Errorf("reputation wallet: nil transaction")
	}
	absChatID := AbsChatID(chatID)
	bkt, err := tx.CreateBucketIfNotExists(bktReputation)
	if err != nil {
		return fmt.Errorf("reputation wallet: %w", err)
	}
	if _, err := ensureMigrated(bkt); err != nil {
		return fmt.Errorf("reputation wallet migration: %w", err)
	}

	account := readAccount(bkt, absChatID, userID)
	nextAvailable, err := checkedAdd(account.Available, available)
	if err != nil {
		return err
	}
	if nextAvailable < 0 {
		return reputation.ErrInsufficientBalance
	}
	nextReserved, err := checkedAdd(account.Reserved, reserved)
	if err != nil {
		return err
	}
	if nextReserved < 0 {
		return reputation.ErrInsufficientReserve
	}
	account.Available = nextAvailable
	account.Reserved = nextReserved
	return writeAccount(bkt, account)
}

func checkedAdd(value, delta int) (int, error) {
	sum := int64(value) + int64(delta)
	if sum > math.MaxInt || sum < math.MinInt {
		return 0, reputation.ErrInvalidAmount
	}
	return int(sum), nil
}

// ReputationRepo implements reputation.Store on top of a single bbolt
// bucket.
type ReputationRepo struct {
	db *bolt.DB
}

// NewReputationRepo constructs a ReputationRepo against the shared
// *bolt.DB. The caller must Close the DB when done.
func NewReputationRepo(db *bolt.DB) *ReputationRepo {
	return &ReputationRepo{db: db}
}

// Wallet implements reputation.Store.Wallet.
func (r *ReputationRepo) Wallet(_ context.Context, chatID, userID int64) (reputation.Account, error) {
	var account reputation.Account
	err := r.db.View(func(tx *bolt.Tx) error {
		var err error
		account, err = ReputationWallet(tx, chatID, userID)
		return err
	})
	return account, err
}

// Settle implements reputation.Store.Settle.
func (r *ReputationRepo) Settle(_ context.Context, req reputation.SocialRequest) (reputation.Result, error) {
	if req.ActorID == req.TargetID {
		return reputation.Result{}, reputation.ErrSelfTarget
	}
	if req.Kind != reputation.KindPraise && req.Kind != reputation.KindRoast {
		return reputation.Result{}, fmt.Errorf("reputation settle: unknown kind %v", req.Kind)
	}

	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	day := now.UTC().Format(repDayFormat)
	absChatID := AbsChatID(req.ChatID)

	var result reputation.Result
	err := r.db.Update(func(tx *bolt.Tx) error {
		bkt, err := tx.CreateBucketIfNotExists(bktReputation)
		if err != nil {
			return fmt.Errorf("create reputation bucket: %w", err)
		}
		if _, err := ensureMigrated(bkt); err != nil {
			return fmt.Errorf("reputation migration: %w", err)
		}
		if err := pruneReputationDays(bkt, absChatID, day); err != nil {
			return err
		}

		if req.SourceMessageID > 0 {
			if bkt.Get(repSourceKey(absChatID, req.ActorID, req.Source, req.SourceMessageID)) != nil {
				return reputation.ErrDuplicateSource
			}
		}
		if bkt.Get(repPairKey(absChatID, day, req.ActorID, req.TargetID)) != nil {
			return reputation.ErrPairLimit
		}
		senderKey := repSenderKey(absChatID, day, req.ActorID)
		senderCount := readCount(bkt.Get(senderKey))
		if senderCount >= repMaxOutgoingPerDay {
			return reputation.ErrSenderLimit
		}

		actor := readAccount(bkt, absChatID, req.ActorID)
		target := readAccount(bkt, absChatID, req.TargetID)
		if actor.Available < 1 {
			return reputation.ErrInsufficientBalance
		}

		nextActor, err := checkedAdd(actor.Available, -1)
		if err != nil {
			return err
		}
		actor.Available = nextActor
		result.ActorWalletDelta = -1

		switch req.Kind {
		case reputation.KindPraise:
			nextAvailable, err := checkedAdd(target.Available, 3)
			if err != nil {
				return err
			}
			nextScore, err := checkedAdd(target.Score, 3)
			if err != nil {
				return err
			}
			target.Available = nextAvailable
			target.Score = nextScore
			result.TargetWalletDelta = 3
			result.TargetScoreDelta = 3

		case reputation.KindRoast:
			// A roast removes at most one unreserved wallet unit and
			// subtracts at most one title point. Reserved funds are not
			// spendable by a negative action.
			if target.Available > 0 {
				target.Available--
				result.TargetWalletDelta = -1
			}
			oldScore := target.Score
			target.Score = max(0, target.Score-1)
			result.TargetScoreDelta = target.Score - oldScore
		}

		if err := writeAccount(bkt, actor); err != nil {
			return fmt.Errorf("write actor wallet: %w", err)
		}
		if err := writeAccount(bkt, target); err != nil {
			return fmt.Errorf("write target wallet: %w", err)
		}

		if err := bkt.Put(repPairKey(absChatID, day, req.ActorID, req.TargetID), repMarker); err != nil {
			return err
		}
		if err := writeCount(bkt, senderKey, senderCount+1); err != nil {
			return err
		}
		if req.SourceMessageID > 0 {
			if err := bkt.Put(repSourceKey(absChatID, req.ActorID, req.Source, req.SourceMessageID), repMarker); err != nil {
				return err
			}
		}

		result.Actor = actor
		result.Target = target
		return nil
	})
	if err != nil {
		return reputation.Result{}, err
	}
	return result, nil
}

// Leaderboard implements reputation.Store.Leaderboard. Only members with
// a positive title score appear; the wallet balance never participates.
func (r *ReputationRepo) Leaderboard(_ context.Context, chatID int64, limit int) ([]reputation.Entry, error) {
	absChatID := AbsChatID(chatID)
	var entries []reputation.Entry
	err := r.db.View(func(tx *bolt.Tx) error {
		bkt := tx.Bucket(bktReputation)
		if bkt == nil {
			entries = []reputation.Entry{}
			return nil
		}

		prefix := repChatPrefix(absChatID)
		c := bkt.Cursor()
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			userID := parseUserIDFromRepKey(k)
			account, _, ok := decodeAccount(v, absChatID, userID)
			if !ok || account.Score <= 0 {
				continue
			}
			entries = append(entries, reputation.Entry{UserID: userID, Score: account.Score})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Sort: title score descending, then user ID ascending for ties.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Score != entries[j].Score {
			return entries[i].Score > entries[j].Score
		}
		return entries[i].UserID < entries[j].UserID
	})

	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	if entries == nil {
		return []reputation.Entry{}, nil
	}
	return entries, nil
}

func readCount(data []byte) int {
	if data == nil {
		return 0
	}
	var rec countRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return 0
	}
	return rec.Count
}

func writeCount(bkt *bolt.Bucket, key []byte, count int) error {
	data, err := json.Marshal(countRecord{Count: count})
	if err != nil {
		return err
	}
	return bkt.Put(key, data)
}

// pruneReputationDays drops this chat's pair and sender counters for
// every day other than today. It runs once per chat per day, so the
// day-scoped limits cannot grow without bound. Source dedup records are
// kept: re-adding an old reaction must never settle again.
func pruneReputationDays(bkt *bolt.Bucket, absChatID int64, today string) error {
	metaKey := repDayMetaKey(absChatID)
	if v := bkt.Get(metaKey); v != nil && string(v) == today {
		return nil
	}
	for _, prefix := range [][]byte{repPairPrefix(absChatID), repSenderPrefix(absChatID)} {
		var stale [][]byte
		c := bkt.Cursor()
		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			parts := bytes.SplitN(k, []byte(":"), 4)
			if len(parts) < 3 || string(parts[2]) == today {
				continue
			}
			stale = append(stale, append([]byte(nil), k...))
		}
		for _, k := range stale {
			if err := bkt.Delete(k); err != nil {
				return err
			}
		}
	}
	return bkt.Put(metaKey, []byte(today))
}

// MigrateReputationChatID rekeys every chat-scoped reputation record from
// oldChatID to newChatID. Existing destination rows win, so a move cannot
// double a wallet. It is safe to call before or after the v1 migration.
func MigrateReputationChatID(_ context.Context, db *bolt.DB, oldChatID, newChatID int64) (int, error) {
	oldAbs := AbsChatID(oldChatID)
	newAbs := AbsChatID(newChatID)
	if oldAbs == 0 || newAbs == 0 || oldAbs == newAbs {
		return 0, nil
	}
	var moved int
	err := db.Update(func(tx *bolt.Tx) error {
		var err error
		moved, err = migrateReputationChat(tx, oldAbs, newAbs)
		return err
	})
	return moved, err
}

// migrateReputationChat is the in-transaction helper for
// storage.MigrateChatID.
func migrateReputationChat(tx *bolt.Tx, oldAbs, newAbs int64) (int, error) {
	bkt := tx.Bucket(bktReputation)
	if bkt == nil {
		return 0, nil
	}
	moved := 0
	for _, prefix := range []string{"r", "rp", "ro", "rs"} {
		n, err := rekeyReputationPrefix(bkt, prefix, oldAbs, newAbs)
		if err != nil {
			return moved, err
		}
		moved += n
	}
	oldMeta := repDayMetaKey(oldAbs)
	newMeta := repDayMetaKey(newAbs)
	if v := bkt.Get(oldMeta); v != nil {
		if bkt.Get(newMeta) == nil {
			if err := bkt.Put(newMeta, v); err != nil {
				return moved, err
			}
		}
		if err := bkt.Delete(oldMeta); err != nil {
			return moved, err
		}
		moved++
	}
	return moved, nil
}

func rekeyReputationPrefix(bkt *bolt.Bucket, prefix string, oldAbs, newAbs int64) (int, error) {
	oldPrefix := []byte(fmt.Sprintf("%s:%020d:", prefix, oldAbs))
	newPrefix := []byte(fmt.Sprintf("%s:%020d:", prefix, newAbs))

	type row struct {
		key []byte
		val []byte
	}
	var rows []row
	c := bkt.Cursor()
	for k, v := c.Seek(oldPrefix); k != nil && bytes.HasPrefix(k, oldPrefix); k, v = c.Next() {
		rows = append(rows, row{
			key: append([]byte(nil), k...),
			val: append([]byte(nil), v...),
		})
	}
	for _, r := range rows {
		newKey := append(append([]byte(nil), newPrefix...), r.key[len(oldPrefix):]...)
		if bkt.Get(newKey) == nil {
			if err := bkt.Put(newKey, r.val); err != nil {
				return 0, err
			}
		}
		if err := bkt.Delete(r.key); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}
