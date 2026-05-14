package profile

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("profile not found")
	ErrExists   = errors.New("profile already exists")
)

type ExpEntry struct {
	Title  string `json:"title"`
	Period string `json:"period"`
}

type SalaryInfo struct {
	Range     string `json:"range"`
	Currency  string `json:"currency"`
	Net       bool   `json:"net"`
	Direction string `json:"direction,omitempty"`
	Status    string `json:"status,omitempty"`
}

type LocationInfo struct {
	City     string `json:"city"`
	Timezone string `json:"timezone,omitempty"`
}

type SetupInfo struct {
	OS      string   `json:"os,omitempty"`
	Devices []string `json:"devices,omitempty"`
	Gaming  []string `json:"gaming,omitempty"`
	AITools []string `json:"ai_tools,omitempty"`
}

type GamingInfo struct {
	Favorites   []string `json:"favorites,omitempty"`
	Preferences string   `json:"preferences,omitempty"`
}

type Profile struct {
	UserID    int64     `json:"user_id"`
	ChatID    int64     `json:"chat_id"`
	Username  string    `json:"username,omitempty"`
	FirstName string    `json:"first_name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Experience []ExpEntry        `json:"experience,omitempty"`
	Stack      []string          `json:"stack,omitempty"`
	Salary     *SalaryInfo       `json:"salary,omitempty"`
	Location   *LocationInfo     `json:"location,omitempty"`
	Setup      *SetupInfo        `json:"setup,omitempty"`
	Links      map[string]string `json:"links,omitempty"`
	Socials    map[string]string `json:"socials,omitempty"`
	Tools      []string          `json:"tools,omitempty"` // deprecated: use Setup.AITools
	Gaming     *GamingInfo       `json:"gaming,omitempty"`
	Media      map[string]string `json:"media,omitempty"`
	Bio        string            `json:"bio,omitempty"`
	Born       int               `json:"born,omitempty"`
	Premium    bool              `json:"premium,omitempty"`
}

type Store interface {
	Create(ctx context.Context, p *Profile) error
	Update(ctx context.Context, p *Profile) error
	Get(ctx context.Context, userID, absChatID int64) (*Profile, error)
	GetByUsername(ctx context.Context, absChatID int64, username string) (*Profile, error)
	ListByChat(ctx context.Context, absChatID int64) ([]Profile, error)
	ListByUser(ctx context.Context, userID int64) ([]Profile, error)
	Exists(ctx context.Context, userID, absChatID int64) (bool, error)
	UpdateUsernameAll(ctx context.Context, userID int64, newUsername string) error
}
