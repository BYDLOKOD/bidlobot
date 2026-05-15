package bot

import (
	"sync"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

// cooldown is a per-(user,key) minimum-interval gate. Games and /stats
// have no natural rate limit, so a single member could flood a
// 200-person chat with /dice or /battle. This bounds invocation
// frequency per user without adding any reply (a "wait" message would
// itself be spam - we just silently drop the over-frequency call).
// cooldownEvictAfter is far longer than any gate window (max 30s), so
// evicting entries older than this never drops a still-relevant
// cooldown but keeps the map from growing for the process lifetime
// (the bot is multi-chat; the key is global per user+command).
const (
	cooldownEvictAfter = 10 * time.Minute
	cooldownSweepEvery = 5 * time.Minute
)

type cooldown struct {
	mu        sync.Mutex
	last      map[string]time.Time // "key|userID" -> last allowed time
	lastSweep time.Time
}

func newCooldown() *cooldown {
	return &cooldown{last: make(map[string]time.Time), lastSweep: time.Now()}
}

func (c *cooldown) allow(userID int64, key string, every time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.sweepLocked(now)
	k := key + "|" + strconvI(userID)
	if t, ok := c.last[k]; ok && now.Sub(t) < every {
		return false
	}
	c.last[k] = now
	return true
}

// sweepLocked drops stale entries at most once per cooldownSweepEvery.
// Caller holds c.mu.
func (c *cooldown) sweepLocked(now time.Time) {
	if now.Sub(c.lastSweep) < cooldownSweepEvery {
		return
	}
	c.lastSweep = now
	for k, t := range c.last {
		if now.Sub(t) > cooldownEvictAfter {
			delete(c.last, k)
		}
	}
}

func strconvI(v int64) string {
	// Tiny inline itoa to avoid a strconv import churn here.
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// gateMsg wraps a message handler so a given user can only trigger it
// once per `every`. Over-frequency calls are dropped silently (no
// reply - the whole point is to reduce chat noise, not add to it).
func (a *App) gateMsg(key string, every time.Duration, h func(*th.Context, telego.Message) error) func(*th.Context, telego.Message) error {
	return func(ctx *th.Context, msg telego.Message) error {
		if msg.From != nil && !a.cooldown.allow(msg.From.ID, key, every) {
			return nil
		}
		return h(ctx, msg)
	}
}
