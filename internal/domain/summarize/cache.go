package summarize

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

const defaultCacheTTL = 10 * time.Minute

type cacheKey struct {
	chatID    int64
	lastMsgID int
	n         int
	qHash     string
}

type cacheEntry struct {
	body    string
	meta    Meta
	created time.Time
}

type cache struct {
	mu      sync.Mutex
	entries map[cacheKey]*cacheEntry
	ttl     time.Duration
}

func newCache(ttl time.Duration) *cache {
	if ttl <= 0 {
		ttl = defaultCacheTTL
	}
	return &cache{
		entries: make(map[cacheKey]*cacheEntry),
		ttl:     ttl,
	}
}

func (c *cache) get(key cacheKey) (string, Meta, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return "", Meta{}, false
	}
	if time.Since(e.created) > c.ttl {
		delete(c.entries, key)
		return "", Meta{}, false
	}
	return e.body, e.meta, true
}

func (c *cache) set(key cacheKey, body string, meta Meta) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, e := range c.entries {
		if now.Sub(e.created) > c.ttl {
			delete(c.entries, k)
		}
	}
	c.entries[key] = &cacheEntry{
		body:    body,
		meta:    meta,
		created: now,
	}
}

func questionsHash(q string) string {
	if q == "" {
		return ""
	}
	h := sha256.Sum256([]byte(q))
	return hex.EncodeToString(h[:])
}
