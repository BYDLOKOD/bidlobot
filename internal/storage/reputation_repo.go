package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/domain/reputation"
)

// Bucket name owned by the reputation domain. It is created on first write
// via CreateBucketIfNotExists so the repo works even before bolt.go
// registers it (and stays correct after it does). For durability the name
// SHOULD also be added to the `buckets` slice in bolt.go - see the wiring
// report in the sequential integration task.
var bktReputation = []byte("reputation")

// defaultBalance returns the starting balance for a user.
func defaultBalance(isAdmin bool) int {
	if isAdmin {
		return 20
	}
	return 10
}

// balanceRecord is the on-disk JSON shape for one user's balance.
type balanceRecord struct {
	Balance int `json:"balance"`
}

// repKey returns the bbolt key for (absChatID, userID).
// Examples: "r:00000000000000000100:00000000000000000001"
func repKey(absChatID, userID int64) []byte {
	return []byte(fmt.Sprintf("r:%020d:%020d", absChatID, userID))
}

// repChatPrefix returns the prefix to scan all users in a chat.
func repChatPrefix(absChatID int64) []byte {
	return []byte(fmt.Sprintf("r:%020d:", absChatID))
}

// parseUserIDFromRepKey extracts the userID from a repKey.
func parseUserIDFromRepKey(key []byte) int64 {
	// Key format: r:CHAT:USER  (split on ':')
	// parts[0]="r", parts[1]="000...100", parts[2]="000...001"
	parts := bytes.Split(key, []byte(":"))
	if len(parts) < 3 {
		return 0
	}
	return parseID(parts[2])
}

// ReputationRepo implements reputation.Store on top of a single bbolt
// bucket. The bucket is lazily created on first write operation.
type ReputationRepo struct {
	db *bolt.DB
}

// NewReputationRepo constructs a ReputationRepo against the shared
// *bolt.DB. The caller must Close the DB when done.
func NewReputationRepo(db *bolt.DB) *ReputationRepo {
	return &ReputationRepo{db: db}
}

// readBalance reads the persisted balance for (chat, user), or returns the
// default when no record exists yet.
func readBalance(bkt *bolt.Bucket, absChatID, userID int64, isAdmin bool) int {
	data := bkt.Get(repKey(absChatID, userID))
	if data == nil {
		return defaultBalance(isAdmin)
	}
	var rec balanceRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		// Corrupt record — treat as fresh and overwrite on next write.
		return defaultBalance(isAdmin)
	}
	return rec.Balance
}

// writeBalance persists a user's balance atomically within the caller's
// transaction.
func writeBalance(bkt *bolt.Bucket, absChatID, userID int64, balance int) error {
	data, err := json.Marshal(balanceRecord{Balance: balance})
	if err != nil {
		return err
	}
	return bkt.Put(repKey(absChatID, userID), data)
}

// Apply implements reputation.Store.Apply.
func (r *ReputationRepo) Apply(_ context.Context, absChatID, actorID, targetID int64, kind reputation.Kind, actorIsAdmin, targetIsAdmin bool) (reputation.Result, error) {
	if actorID == targetID {
		return reputation.Result{}, reputation.ErrSelfTarget
	}

	var result reputation.Result
	err := r.db.Update(func(tx *bolt.Tx) error {
		bkt, err := tx.CreateBucketIfNotExists(bktReputation)
		if err != nil {
			return fmt.Errorf("create reputation bucket: %w", err)
		}

		actorBal := readBalance(bkt, absChatID, actorID, actorIsAdmin)
		targetBal := readBalance(bkt, absChatID, targetID, targetIsAdmin)

		switch kind {
		case reputation.KindPraise:
			if actorBal < 1 {
				return reputation.ErrInsufficientBalance
			}
			actorBal--
			if targetIsAdmin {
				targetBal += 6
			} else {
				targetBal += 3
			}

		case reputation.KindRoast:
			if actorBal < 1 {
				return reputation.ErrInsufficientBalance
			}
			if targetBal < 1 {
				return reputation.ErrTargetInsufficientBalance
			}
			actorBal--
			targetBal--

		default:
			return fmt.Errorf("reputation repo: unknown kind %v", kind)
		}

		if err := writeBalance(bkt, absChatID, actorID, actorBal); err != nil {
			return fmt.Errorf("write actor balance: %w", err)
		}
		if err := writeBalance(bkt, absChatID, targetID, targetBal); err != nil {
			return fmt.Errorf("write target balance: %w", err)
		}

		result = reputation.Result{ActorBalance: actorBal, TargetBalance: targetBal}
		return nil
	})
	if err != nil {
		return reputation.Result{}, err
	}
	return result, nil
}

// Balance implements reputation.Store.Balance. Lazily creates the
// reputation record on first read so the default is durable after
// the first /rep command.
func (r *ReputationRepo) Balance(_ context.Context, absChatID, userID int64, isAdmin bool) (int, error) {
	var bal int
	err := r.db.Update(func(tx *bolt.Tx) error {
		bkt, err := tx.CreateBucketIfNotExists(bktReputation)
		if err != nil {
			return fmt.Errorf("reputation balance: %w", err)
		}
		bal = readBalance(bkt, absChatID, userID, isAdmin)
		// Persist the lazy init so subsequent reads don't re-derive default.
		return writeBalance(bkt, absChatID, userID, bal)
	})
	return bal, err
}

// Leaderboard implements reputation.Store.Leaderboard. Scans the
// entire chat prefix and sorts in-memory.
func (r *ReputationRepo) Leaderboard(_ context.Context, absChatID int64, limit int) ([]reputation.Entry, error) {
	var entries []reputation.Entry
	err := r.db.View(func(tx *bolt.Tx) error {
		bkt := tx.Bucket(bktReputation)
		if bkt == nil {
			entries = []reputation.Entry{}
			return nil
		}

		c := bkt.Cursor()
		prefix := repChatPrefix(absChatID)

		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			var rec balanceRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				continue // skip corrupt rows
			}
			userID := parseUserIDFromRepKey(k)
			entries = append(entries, reputation.Entry{UserID: userID, Balance: rec.Balance})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Sort: balance descending, then user ID ascending for ties.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Balance != entries[j].Balance {
			return entries[i].Balance > entries[j].Balance
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
