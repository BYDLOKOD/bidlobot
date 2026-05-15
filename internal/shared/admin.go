package shared

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/mymmrac/telego"
)

const groupAnonymousBotID int64 = 1087968824

type adminEntry struct {
	admins      map[int64]struct{}
	canRestrict bool
	fetchedAt   time.Time
}

type AdminCache struct {
	mu      sync.RWMutex
	entries map[int64]*adminEntry
	ttl     time.Duration
	api     TelegramAPI
	botID   int64
	log     *slog.Logger
}

func NewAdminCache(api TelegramAPI, botID int64, log *slog.Logger) *AdminCache {
	return &AdminCache{
		entries: make(map[int64]*adminEntry),
		ttl:     60 * time.Second,
		api:     api,
		botID:   botID,
		log:     log,
	}
}

func (c *AdminCache) IsAdmin(absChatID int64, userID int64) (bool, error) {
	entry, err := c.getOrFetch(absChatID)
	if err != nil {
		return false, err
	}
	_, ok := entry.admins[userID]
	return ok, nil
}

func (c *AdminCache) BotCanRestrict(absChatID int64) (bool, error) {
	entry, err := c.getOrFetch(absChatID)
	if err != nil {
		return false, err
	}
	return entry.canRestrict, nil
}

func (c *AdminCache) Invalidate(absChatID int64) {
	c.mu.Lock()
	delete(c.entries, absChatID)
	c.mu.Unlock()
}

func (c *AdminCache) getOrFetch(absChatID int64) (*adminEntry, error) {
	c.mu.RLock()
	e, ok := c.entries[absChatID]
	if ok && time.Since(e.fetchedAt) < c.ttl {
		c.mu.RUnlock()
		return e, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok = c.entries[absChatID]
	if ok && time.Since(e.fetchedAt) < c.ttl {
		return e, nil
	}

	chatID := -absChatID

	members, err := c.api.GetChatAdministrators(context.Background(), &telego.GetChatAdministratorsParams{
		ChatID: telego.ChatID{ID: chatID},
	})
	if err != nil {
		return nil, err
	}

	entry := &adminEntry{
		admins:    make(map[int64]struct{}),
		fetchedAt: time.Now(),
	}

	for _, m := range members {
		user := m.MemberUser()
		if user.IsBot {
			if user.ID == c.botID {
				if admin, ok := m.(*telego.ChatMemberAdministrator); ok {
					entry.canRestrict = admin.CanRestrictMembers
				}
			}
			continue
		}
		entry.admins[user.ID] = struct{}{}
	}

	c.entries[absChatID] = entry
	return entry, nil
}

func IsAnonymousAdmin(userID int64) bool {
	return userID == groupAnonymousBotID
}
