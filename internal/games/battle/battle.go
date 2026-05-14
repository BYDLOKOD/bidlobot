// Package battle implements the reaction-battle mini-game.
//
// A battle has two sides (X and Y, labelled by the inviter) and a fixed
// 60-second voting window. The bot posts one tracking message per side;
// each non-bot user who reacts (any reaction emoji) once to a side
// counts as one vote for that side. Reactions are removed do NOT
// decrement - the simplest tally is "ever reacted" per (user, side),
// which avoids race conditions with the unsubscribed reaction stream.
//
// State is kept entirely in memory: battles last 60 seconds and there
// is no value in persisting them across restarts. The Store API exists
// only so the bot package can register tracking messages without
// importing battle's internal mutex layout.
package battle

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// DefaultDuration is the voting window length. 60s is a balance between
// "enough time for a 200-member chat to notice" and "no one wants to
// wait for the result".
const DefaultDuration = 60 * time.Second

// MaxLabelLen caps the per-side label so the announcement message
// stays compact and Telegram's 4096-byte body limit is never a concern.
const MaxLabelLen = 32

var (
	// ErrBadLabels signals an invalid X or Y argument (empty after trim
	// or longer than MaxLabelLen).
	ErrBadLabels = errors.New("battle: invalid labels")

	// ErrNotFound is returned by Store.Get when neither side of the
	// requested message ID belongs to a known battle.
	ErrNotFound = errors.New("battle: not found")
)

// Side identifies one of the two battle sides.
type Side int

const (
	SideLeft  Side = 0
	SideRight Side = 1
)

// Battle is the in-memory record of an active vote.
type Battle struct {
	ID        string
	AbsChatID int64
	LeftLabel string
	RightLeft string

	// LeftMessageID and RightMessageID are the Telegram message_ids
	// users react to. Either may be 0 transiently while messages are
	// being posted, but Tally never reads a battle until both are set.
	LeftMessageID  int
	RightMessageID int

	StartedAt time.Time
	EndsAt    time.Time

	mu        sync.Mutex
	leftVoters  map[int64]struct{}
	rightVoters map[int64]struct{}
}

// Result is what Tally returns at the end of a battle.
type Result struct {
	BattleID    string
	LeftLabel   string
	RightLabel  string
	LeftVotes   int
	RightVotes  int
	WinnerSide  Side  // SideLeft or SideRight; ignored when Tied
	Tied        bool
	NoVotes     bool  // true when LeftVotes==RightVotes==0
	StartedAt   time.Time
	FinishedAt  time.Time
}

// NewBattle constructs a Battle and its empty voter sets. ID generation
// is the caller's responsibility (the storage package owns crypto/rand).
func NewBattle(id string, absChatID int64, leftLabel, rightLabel string, startedAt time.Time, duration time.Duration) (*Battle, error) {
	left := strings.TrimSpace(leftLabel)
	right := strings.TrimSpace(rightLabel)
	if left == "" || right == "" {
		return nil, ErrBadLabels
	}
	if len(left) > MaxLabelLen || len(right) > MaxLabelLen {
		return nil, fmt.Errorf("%w: max label length is %d", ErrBadLabels, MaxLabelLen)
	}
	if duration <= 0 {
		duration = DefaultDuration
	}
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	return &Battle{
		ID:          id,
		AbsChatID:   absChatID,
		LeftLabel:   left,
		RightLeft:   right,
		StartedAt:   startedAt.UTC(),
		EndsAt:      startedAt.UTC().Add(duration),
		leftVoters:  make(map[int64]struct{}),
		rightVoters: make(map[int64]struct{}),
	}, nil
}

// RecordVote registers a vote from userID on side. Returns true when
// the vote was new (i.e. the user had not previously voted on that side
// in this battle), false when ignored. Bot users and zero IDs are
// silently rejected by callers; this function trusts validation.
func (b *Battle) RecordVote(userID int64, side Side) bool {
	if userID == 0 {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	switch side {
	case SideLeft:
		if _, ok := b.leftVoters[userID]; ok {
			return false
		}
		b.leftVoters[userID] = struct{}{}
	case SideRight:
		if _, ok := b.rightVoters[userID]; ok {
			return false
		}
		b.rightVoters[userID] = struct{}{}
	default:
		return false
	}
	return true
}

// Tally returns the current result without finalising the battle.
// Callers can re-tally for a live progress update as well as at the end
// of the timer.
func (b *Battle) Tally(now time.Time) *Result {
	b.mu.Lock()
	defer b.mu.Unlock()
	left := len(b.leftVoters)
	right := len(b.rightVoters)
	r := &Result{
		BattleID:   b.ID,
		LeftLabel:  b.LeftLabel,
		RightLabel: b.RightLeft,
		LeftVotes:  left,
		RightVotes: right,
		StartedAt:  b.StartedAt,
		FinishedAt: now.UTC(),
	}
	switch {
	case left == 0 && right == 0:
		r.NoVotes = true
		r.Tied = true
	case left == right:
		r.Tied = true
	case left > right:
		r.WinnerSide = SideLeft
	default:
		r.WinnerSide = SideRight
	}
	return r
}

// IsLeftMessage and IsRightMessage are used by the registry to find the
// battle a reaction belongs to.
func (b *Battle) IsLeftMessage(msgID int) bool  { return msgID != 0 && msgID == b.LeftMessageID }
func (b *Battle) IsRightMessage(msgID int) bool { return msgID != 0 && msgID == b.RightMessageID }

// Registry is the thread-safe in-memory store of active battles.
// Battles are addressable both by their ID (for the goroutine that
// finishes them) and by their per-side message ID (for the reaction
// observer).
type Registry struct {
	mu       sync.RWMutex
	byID     map[string]*Battle
	byLeftMsg  map[int]*Battle
	byRightMsg map[int]*Battle
}

func NewRegistry() *Registry {
	return &Registry{
		byID:       make(map[string]*Battle),
		byLeftMsg:  make(map[int]*Battle),
		byRightMsg: make(map[int]*Battle),
	}
}

// Add inserts a battle. After Add the registry holds the battle by ID
// only; SetMessageIDs must be called once both side messages have been
// posted to wire up reaction-observer lookups.
func (r *Registry) Add(b *Battle) {
	if b == nil || b.ID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[b.ID] = b
}

// SetMessageIDs records the per-side Telegram message IDs and registers
// the battle with the reaction-observer index. Calling SetMessageIDs
// twice for the same battle is allowed (handler retry); the previous
// message-ID entries are removed first.
func (r *Registry) SetMessageIDs(id string, leftMsgID, rightMsgID int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.byID[id]
	if !ok {
		return
	}
	if b.LeftMessageID != 0 {
		delete(r.byLeftMsg, b.LeftMessageID)
	}
	if b.RightMessageID != 0 {
		delete(r.byRightMsg, b.RightMessageID)
	}
	b.LeftMessageID = leftMsgID
	b.RightMessageID = rightMsgID
	if leftMsgID != 0 {
		r.byLeftMsg[leftMsgID] = b
	}
	if rightMsgID != 0 {
		r.byRightMsg[rightMsgID] = b
	}
}

// LookupByMessageID returns the battle and the side a reaction message
// belongs to. ok is false when no battle owns that message.
func (r *Registry) LookupByMessageID(msgID int) (b *Battle, side Side, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if v, found := r.byLeftMsg[msgID]; found {
		return v, SideLeft, true
	}
	if v, found := r.byRightMsg[msgID]; found {
		return v, SideRight, true
	}
	return nil, 0, false
}

// Get returns the battle by ID, or nil if not found.
func (r *Registry) Get(id string) *Battle {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byID[id]
}

// Remove evicts a battle from all indices. Idempotent.
func (r *Registry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.byID[id]
	if !ok {
		return
	}
	delete(r.byID, id)
	if b.LeftMessageID != 0 {
		delete(r.byLeftMsg, b.LeftMessageID)
	}
	if b.RightMessageID != 0 {
		delete(r.byRightMsg, b.RightMessageID)
	}
}

// Active returns the count of in-flight battles. Useful for tests and
// future /battle-status diagnostics.
func (r *Registry) Active() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byID)
}
