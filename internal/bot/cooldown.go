package bot

import (
	"strconv"
	"fmt"
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
	notified  map[string]time.Time // "key|userID" -> last "slow down" notice
	lastSweep time.Time
}

func newCooldown() *cooldown {
	return &cooldown{
		last:      make(map[string]time.Time),
		notified:  make(map[string]time.Time),
		lastSweep: time.Now(),
	}
}

// gate decides whether an invocation is allowed and, if not, whether the
// caller should emit ONE "slow down" notice. The notice is bounded to at
// most once per `every` window per (user,key): a tester gets feedback
// instead of silence, while a real flooder still cannot amplify (one
// result + one notice per window, then silence). A fresh allow resets
// the notice state so the next over-frequency burst is acknowledged.
func (c *cooldown) gate(userID int64, key string, every time.Duration) (allowed, notify bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.sweepLocked(now)
	k := key + "|" + strconv.FormatInt(userID, 10)
	if t, ok := c.last[k]; ok && now.Sub(t) < every {
		if n, seen := c.notified[k]; !seen || now.Sub(n) >= every {
			c.notified[k] = now
			return false, true
		}
		return false, false
	}
	c.last[k] = now
	delete(c.notified, k)
	return true, false
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
			delete(c.notified, k)
		}
	}
	for k, t := range c.notified {
		if now.Sub(t) > cooldownEvictAfter {
			delete(c.notified, k)
		}
	}
}


// gateMsg wraps a message handler so a given user can only trigger it
// once per `every`. Over-frequency calls are dropped silently (no
// reply - the whole point is to reduce chat noise, not add to it).
func (a *App) gateMsg(key string, every time.Duration, h func(*th.Context, telego.Message) error) func(*th.Context, telego.Message) error {
	return func(ctx *th.Context, msg telego.Message) error {
		if msg.From == nil {
			return h(ctx, msg)
		}
		allowed, notify := a.cooldown.gate(msg.From.ID, key, every)
		if allowed {
			return h(ctx, msg)
		}
		// Over-frequency: stay silent EXCEPT for one bounded notice per
		// window, so a tester (or any user) learns why nothing happened
		// instead of assuming the bot is broken - while a real flooder
		// still cannot amplify (>=1 result + <=1 notice per window, then
		// silence). Sent through the rate-limited wrapper; nil-guarded
		// for minimal/test apps.
		if notify && a.sender != nil {
			secs := int(every.Seconds())
			if secs < 1 {
				secs = 1
			}
			_, _ = a.sender.SendMessage(ctx.Context(), &telego.SendMessageParams{
				ChatID:          telego.ChatID{ID: msg.Chat.ID},
				Text:            fmt.Sprintf("⏳ Не части - /%s доступна раз в %d c.", key, secs),
				ReplyParameters: &telego.ReplyParameters{MessageID: msg.GetMessageID()},
			})
		}
		return nil
	}
}
