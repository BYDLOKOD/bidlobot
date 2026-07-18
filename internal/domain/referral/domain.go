// Package referral is the chat-scoped referral catalog domain.
//
// A Service is a chat-local category (e.g. "ZAI Coding Plan") that
// owns zero or more Referral entries posted by chat members. The
// catalog is keyed by absolute chat ID everywhere: storage, matching,
// and moderation are all per-supergroup, never global.
package referral

import (
	"context"
	"errors"
	"time"
)

// Service is one chat-local category. NameKey is the NormalizeName
// reduction of Name; callers must recompute it on read rather than
// trusting caller-supplied values when matching.
type Service struct {
	ID        uint64    `json:"id"`
	AbsChatID int64     `json:"abs_chat_id"`
	Name      string    `json:"name"`
	Effect    string    `json:"effect,omitempty"`
	NameKey   string    `json:"name_key"`
	CreatedAt time.Time `json:"created_at"`
}

// Referral is one member-posted link under a Service.
type Referral struct {
	ID           uint64    `json:"id"`
	AbsChatID    int64     `json:"abs_chat_id"`
	ServiceID    uint64    `json:"service_id"`
	OwnerUserID  int64     `json:"owner_user_id"`
	OwnerDisplay string    `json:"owner_display"`
	URL          string    `json:"url"`
	CreatedAt    time.Time `json:"created_at"`
}

// Group is one Service plus every Referral filed under it, in the
// shape the listing UX renders.
type Group struct {
	Service   Service
	Referrals []Referral
}

// Store is the persistence contract. Every int64 is the absolute chat
// ID. Create sets both stored AbsChatID fields from the chat argument
// regardless of what the caller supplied, so chat-scoping cannot drift
// through a stale template.
type Store interface {
	// Create persists a referral and the service it belongs to.
	//
	// If svc.ID != 0 the existing service is loaded and used; a missing
	// service returns ErrNotFound. If svc.ID == 0 a new service is
	// inserted only when no service with the same NameKey already
	// exists in this chat — a concurrent exact category returns
	// ErrServiceExists. A second referral by the same owner under the
	// same service returns ErrOwnerServiceExists, and a URL already
	// present anywhere in the chat returns ErrURLExists.
	Create(ctx context.Context, absChatID int64, svc Service, ref Referral) (*Service, *Referral, error)

	// List returns every service and referral in the chat, grouped by
	// service. Services sort case-insensitively by Name; referrals
	// sort by ID.
	List(ctx context.Context, absChatID int64) ([]Group, error)

	// GetReferral loads one referral by ID, scoped to absChatID.
	GetReferral(ctx context.Context, absChatID int64, id uint64) (*Referral, error)

	// DeleteReferral removes one referral, and prunes its service plus
	// the service's name index when no referrals remain under it.
	DeleteReferral(ctx context.Context, absChatID int64, id uint64) error
}

// Sentinel errors returned by Store implementations.
var (
	ErrNotFound           = errors.New("referral: not found")
	ErrServiceExists      = errors.New("referral: service already exists")
	ErrOwnerServiceExists = errors.New("referral: owner already has a referral for this service")
	ErrURLExists          = errors.New("referral: url already exists in this chat")
)
