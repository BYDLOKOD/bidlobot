package membership

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("member not found")
	ErrChatNotFound = errors.New("chat not found")
)

// Source describes which Telegram event taught the bot about a member.
// Used for diagnostics and to distinguish "we have rich data" from
// "we only saw a single ChatMember status update".
type Source string

const (
	SourceMessage     Source = "message"
	SourceReaction    Source = "reaction"
	SourceChatMember  Source = "chat_member"
	SourceMyChatAdmin Source = "my_chat_admin"
)

// Status mirrors Telegram's ChatMember status enum, narrowed to the values
// we actually care about.
type Status string

const (
	StatusUnknown       Status = ""
	StatusCreator       Status = "creator"
	StatusAdministrator Status = "administrator"
	StatusMember        Status = "member"
	StatusRestricted    Status = "restricted"
	StatusLeft          Status = "left"
	StatusKicked        Status = "kicked"
)

// Member is a per-chat record about an observed user. The bot can only
// learn about a user when it observes one of their actions (message,
// reaction) or a chat_member status update - Bot API has no enumeration.
type Member struct {
	UserID         int64     `json:"user_id"`
	AbsChatID      int64     `json:"abs_chat_id"`
	Username       string    `json:"username,omitempty"`
	FirstName      string    `json:"first_name,omitempty"`
	IsBot          bool      `json:"is_bot,omitempty"`
	IsPremium      bool      `json:"is_premium,omitempty"`
	JoinedAt       time.Time `json:"joined_at,omitempty"`
	LeftAt         time.Time `json:"left_at,omitempty"`
	LastMessageAt  time.Time `json:"last_message_at,omitempty"`
	LastReactionAt time.Time `json:"last_reaction_at,omitempty"`
	LastSeenAt     time.Time `json:"last_seen_at"`
	FirstSeenAt    time.Time `json:"first_seen_at"`
	MessageCount   int64     `json:"message_count"`
	ReactionCount  int64     `json:"reaction_count"`
	Status         Status    `json:"status"`
	KnownVia       Source    `json:"known_via"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Chat records a chat the bot is installed in. Lets background jobs (e.g.
// periodic cleanup reminders) iterate without re-discovering installations
// every restart.
type Chat struct {
	AbsChatID    int64     `json:"abs_chat_id"`
	Title        string    `json:"title,omitempty"`
	Type         string    `json:"type"`
	BotStatus    Status    `json:"bot_status"`
	CanRestrict  bool      `json:"can_restrict"`
	CanDelete    bool      `json:"can_delete"`
	InstalledAt  time.Time `json:"installed_at"`
	LastUpdateAt time.Time `json:"last_update_at"`
}

// MemberPatch carries only the fields a single event can update. Zero
// values mean "leave this field untouched"; an upsert merges the patch
// into the existing record (or creates one).
type MemberPatch struct {
	UserID    int64
	AbsChatID int64

	Username  *string
	FirstName *string
	IsBot     *bool
	IsPremium *bool

	Status   Status // empty = no change
	KnownVia Source

	JoinedAt       time.Time
	LeftAt         time.Time
	LastMessageAt  time.Time
	LastReactionAt time.Time

	IncMessageCount  int64
	IncReactionCount int64

	Now time.Time // event timestamp; falls back to time.Now() when zero
}

// Store is the contract for all membership persistence.
type Store interface {
	UpsertMember(ctx context.Context, p MemberPatch) (*Member, error)
	GetMember(ctx context.Context, userID, absChatID int64) (*Member, error)
	GetMemberByUsername(ctx context.Context, absChatID int64, username string) (*Member, error)
	ListByChat(ctx context.Context, absChatID int64) ([]Member, error)

	UpsertChat(ctx context.Context, c Chat) error
	GetChat(ctx context.Context, absChatID int64) (*Chat, error)
	ListChats(ctx context.Context) ([]Chat, error)
}
