package quiz

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrNotFound is returned by Store.GetEntry when the requested
// (chat, user) pair has no recorded score yet.
var ErrNotFound = errors.New("quiz: leaderboard entry not found")

// Entry is one user's leaderboard standing in one chat.
type Entry struct {
	AbsChatID    int64     `json:"abs_chat_id"`
	UserID       int64     `json:"user_id"`
	Username     string    `json:"username,omitempty"`
	FirstName    string    `json:"first_name,omitempty"`
	CorrectCount int64     `json:"correct_count"`
	LastPlayedAt time.Time `json:"last_played_at"`
}

// Store is the persistence contract for the per-chat leaderboard.
// IncrementCorrect both creates and updates: callers do not need to
// pre-check existence.
type Store interface {
	IncrementCorrect(ctx context.Context, e Entry) error
	GetEntry(ctx context.Context, absChatID, userID int64) (*Entry, error)
	TopByChat(ctx context.Context, absChatID int64, limit int) ([]Entry, error)
}

// ActiveQuizzes tracks in-flight quiz messages keyed by Telegram
// message_id. Solving a quiz is "first correct tap wins"; the rest of
// the buttons either show a "wrong guess" toast or, after the quiz is
// solved, an "already solved" toast.
//
// State lives only in memory. A bot restart cancels in-flight quizzes
// (the buttons keep working but no leaderboard credit is awarded).
// Acceptable trade-off: a code quiz in a 200-member chat resolves in
// seconds.
type ActiveQuizzes struct {
	mu    sync.Mutex
	byMsg map[int64]*ActiveQuiz // key: messageID
}

// ActiveQuiz captures everything the callback handler needs to validate
// a tap and award credit.
type ActiveQuiz struct {
	MessageID  int64
	AbsChatID  int64
	SnippetIdx int
	CorrectIdx int    // index into Options (0..3)
	Options    []Lang // the four guess buttons in displayed order
	StartedAt  time.Time
	WinnerID   int64  // 0 until first correct tap; nonzero after
	WinnerName string // display name of the winner
}

func NewActiveQuizzes() *ActiveQuizzes {
	return &ActiveQuizzes{byMsg: make(map[int64]*ActiveQuiz)}
}

// Register inserts a quiz keyed by message_id. Overwrites any prior
// entry with the same key (which would only happen on a wildly
// improbable Telegram message_id collision).
func (a *ActiveQuizzes) Register(q *ActiveQuiz) {
	if q == nil || q.MessageID == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.byMsg[q.MessageID] = q
}

// Get returns the active quiz for a message_id, or nil if not tracked.
func (a *ActiveQuizzes) Get(messageID int64) *ActiveQuiz {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.byMsg[messageID]
}

// MarkSolved atomically marks the quiz as solved by userID/name. Returns
// true if the caller is the first solver, false if someone else got
// there first.
func (a *ActiveQuizzes) MarkSolved(messageID int64, userID int64, displayName string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	q, ok := a.byMsg[messageID]
	if !ok {
		return false
	}
	if q.WinnerID != 0 {
		return false
	}
	q.WinnerID = userID
	q.WinnerName = displayName
	return true
}

// Forget removes a tracked quiz. Called when the quiz is solved (or
// when explicit cleanup wants to reclaim the memory).
func (a *ActiveQuizzes) Forget(messageID int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.byMsg, messageID)
}

// Active returns the count of in-flight quizzes. Mostly for tests.
func (a *ActiveQuizzes) Active() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.byMsg)
}
