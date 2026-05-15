// Package tgclient wraps *telego.Bot with three production-readiness
// concerns layered in this exact order, outermost first:
//
//  1. migration handler - on a 400 with parameters.migrate_to_chat_id,
//     rewrite all DB records keyed by the old chat id and retry with
//     the new chat id. Runs once per call.
//
//  2. retry with backoff - on 429 sleep retry_after+jitter once; on 5xx
//     follow the bounded exponential ladder; other 4xx surface as-is.
//
//  3. per-chat rate limiter - 15 req/min per chat with a FIFO queue
//     bounded at 50; overflow drops oldest with a WARN log.
//
// The composition is "migration outside retry outside rate-limit": once
// the rate-limit grants a slot we may consume one or more attempts via
// the retry policy, but a migration replays the entire chain against the
// new chat id so its rate budget is also fresh.
//
// The wrapper exposes the minimal subset of telego methods used by the
// bot. It does not implement every Telegram method - read-only methods
// (GetMe, GetChat, GetChatMember, GetChatAdministrators) bypass rate
// limiting because they do not produce visible chat traffic and are
// usually called from background paths the user can absorb a small
// hiccup on.
package tgclient

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/mymmrac/telego"
	"github.com/mymmrac/telego/telegoapi"

	"github.com/veschin/bidlobot/internal/shared"
	"github.com/veschin/bidlobot/internal/shared/ratelimit"
	"github.com/veschin/bidlobot/internal/shared/retry"
)

// Migrator persists the chat-id rewrite. Defined as an interface so the
// production storage and a test fake can both satisfy it.
type Migrator interface {
	MigrateChatID(ctx context.Context, oldAbs, newAbs int64) error
}

// AdminInvalidator clears any per-chat caches that key off the old chat id.
type AdminInvalidator interface {
	Invalidate(absChatID int64)
}

// Client is the fully wrapped Telegram bot client. Construct with [New].
type Client struct {
	bot         *telego.Bot
	limiter     *ratelimit.Limiter
	retryPolicy retry.Policy
	migrator    Migrator
	admin       AdminInvalidator
	log         *slog.Logger
}

// Config bundles dependencies; nothing is optional except Logger (which
// defaults to slog.Default) - a wrapper that silently skipped a layer
// would defeat the point of the wrapper.
type Config struct {
	Bot         *telego.Bot
	Limiter     *ratelimit.Limiter
	RetryPolicy retry.Policy
	Migrator    Migrator
	Admin       AdminInvalidator
	Logger      *slog.Logger
}

// New validates config and returns a Client. Returns an error rather than
// panicking so main can decide whether to crash hard or fall back.
func New(cfg Config) (*Client, error) {
	if cfg.Bot == nil {
		return nil, errors.New("tgclient: nil bot")
	}
	if cfg.Limiter == nil {
		return nil, errors.New("tgclient: nil limiter")
	}
	if cfg.Migrator == nil {
		return nil, errors.New("tgclient: nil migrator")
	}
	if cfg.Admin == nil {
		return nil, errors.New("tgclient: nil admin invalidator")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Client{
		bot:         cfg.Bot,
		limiter:     cfg.Limiter,
		retryPolicy: cfg.RetryPolicy,
		migrator:    cfg.Migrator,
		admin:       cfg.Admin,
		log:         cfg.Logger,
	}, nil
}

// Bot returns the underlying *telego.Bot for read-only methods that do
// not need rate limiting or retry. Callers should prefer the wrapped
// methods on Client when sending anything user-facing.
func (c *Client) Bot() *telego.Bot { return c.bot }

// runWrite executes a write through limiter -> retry -> bot, with a
// migration handler around the whole thing. signedChatID is the value
// embedded in the request (negative for groups). migrateChatID is invoked
// when Telegram redirects: it should rewrite the request params and
// return whether to replay.
func (c *Client) runWrite(
	ctx context.Context,
	chatID int64,
	method string,
	send func(ctx context.Context) error,
	migrateApply func(newSignedChatID int64),
) error {
	abs := absInt64(chatID)
	const maxMigrations = 2 // pathological: a chain of migrations should not loop forever
	for i := 0; i < maxMigrations; i++ {
		if err := c.limiter.Wait(ctx, abs); err != nil {
			return err
		}

		var apiErr *telegoapi.Error
		err := retry.Do(ctx, c.retryPolicy, func(ctx context.Context) error {
			return send(ctx)
		})
		if err == nil {
			return nil
		}

		if !errors.As(err, &apiErr) {
			return err
		}
		if apiErr.ErrorCode != 400 || apiErr.Parameters == nil || apiErr.Parameters.MigrateToChatID == 0 {
			return err
		}

		// Migration redirect: rewrite DB and rebuild request.
		newSigned := apiErr.Parameters.MigrateToChatID
		newAbs := absInt64(newSigned)
		oldAbs := abs

		c.log.Info("telegram chat migrated", "method", method,
			"old_chat_id", chatID, "new_chat_id", newSigned,
			"old_abs", oldAbs, "new_abs", newAbs)

		if err := c.migrator.MigrateChatID(ctx, oldAbs, newAbs); err != nil {
			return fmt.Errorf("apply migration old=%d new=%d: %w", oldAbs, newAbs, err)
		}
		c.admin.Invalidate(oldAbs)

		// Apply new id to the request and replay.
		migrateApply(newSigned)
		chatID = newSigned
		abs = newAbs
	}
	return fmt.Errorf("tgclient: migration loop exceeded for method %s", method)
}

// SendMessage wraps telego.Bot.SendMessage.
func (c *Client) SendMessage(ctx context.Context, params *telego.SendMessageParams) (*telego.Message, error) {
	if params == nil {
		return nil, errors.New("tgclient: nil params")
	}
	var msg *telego.Message
	err := c.runWrite(ctx, params.ChatID.ID, "sendMessage",
		func(ctx context.Context) error {
			m, e := c.bot.SendMessage(ctx, params)
			if e != nil {
				return e
			}
			msg = m
			return nil
		},
		func(newSigned int64) { params.ChatID = telego.ChatID{ID: newSigned} },
	)
	return msg, err
}

// SendDice wraps telego.Bot.SendDice. Dice is a chat-visible message so
// it shares the per-chat rate budget with every other public send -
// otherwise a flood of /dice would blow past Telegram's 20/min/chat.
func (c *Client) SendDice(ctx context.Context, params *telego.SendDiceParams) (*telego.Message, error) {
	if params == nil {
		return nil, errors.New("tgclient: nil params")
	}
	var msg *telego.Message
	err := c.runWrite(ctx, params.ChatID.ID, "sendDice",
		func(ctx context.Context) error {
			m, e := c.bot.SendDice(ctx, params)
			if e != nil {
				return e
			}
			msg = m
			return nil
		},
		func(newSigned int64) { params.ChatID = telego.ChatID{ID: newSigned} },
	)
	return msg, err
}

// SendPoll wraps telego.Bot.SendPoll. A poll is a chat-visible message
// so it shares the per-chat rate budget like the other public games.
func (c *Client) SendPoll(ctx context.Context, params *telego.SendPollParams) (*telego.Message, error) {
	if params == nil {
		return nil, errors.New("tgclient: nil params")
	}
	var msg *telego.Message
	err := c.runWrite(ctx, params.ChatID.ID, "sendPoll",
		func(ctx context.Context) error {
			m, e := c.bot.SendPoll(ctx, params)
			if e != nil {
				return e
			}
			msg = m
			return nil
		},
		func(newSigned int64) { params.ChatID = telego.ChatID{ID: newSigned} },
	)
	return msg, err
}

// SendPhoto wraps telego.Bot.SendPhoto. A photo is a chat-visible
// message so it shares the per-chat rate budget; the YouTube link
// sanitizer reposts media by file_id through this path rather than the
// raw bot so a burst of sanitized posts cannot exceed 15/min/chat.
func (c *Client) SendPhoto(ctx context.Context, params *telego.SendPhotoParams) (*telego.Message, error) {
	if params == nil {
		return nil, errors.New("tgclient: nil params")
	}
	var msg *telego.Message
	err := c.runWrite(ctx, params.ChatID.ID, "sendPhoto",
		func(ctx context.Context) error {
			m, e := c.bot.SendPhoto(ctx, params)
			if e != nil {
				return e
			}
			msg = m
			return nil
		},
		func(newSigned int64) { params.ChatID = telego.ChatID{ID: newSigned} },
	)
	return msg, err
}

// SendVideo wraps telego.Bot.SendVideo. See SendPhoto for the
// rate-budget rationale.
func (c *Client) SendVideo(ctx context.Context, params *telego.SendVideoParams) (*telego.Message, error) {
	if params == nil {
		return nil, errors.New("tgclient: nil params")
	}
	var msg *telego.Message
	err := c.runWrite(ctx, params.ChatID.ID, "sendVideo",
		func(ctx context.Context) error {
			m, e := c.bot.SendVideo(ctx, params)
			if e != nil {
				return e
			}
			msg = m
			return nil
		},
		func(newSigned int64) { params.ChatID = telego.ChatID{ID: newSigned} },
	)
	return msg, err
}

// SendAnimation wraps telego.Bot.SendAnimation. See SendPhoto for the
// rate-budget rationale.
func (c *Client) SendAnimation(ctx context.Context, params *telego.SendAnimationParams) (*telego.Message, error) {
	if params == nil {
		return nil, errors.New("tgclient: nil params")
	}
	var msg *telego.Message
	err := c.runWrite(ctx, params.ChatID.ID, "sendAnimation",
		func(ctx context.Context) error {
			m, e := c.bot.SendAnimation(ctx, params)
			if e != nil {
				return e
			}
			msg = m
			return nil
		},
		func(newSigned int64) { params.ChatID = telego.ChatID{ID: newSigned} },
	)
	return msg, err
}

// SendDocument wraps telego.Bot.SendDocument. See SendPhoto for the
// rate-budget rationale.
func (c *Client) SendDocument(ctx context.Context, params *telego.SendDocumentParams) (*telego.Message, error) {
	if params == nil {
		return nil, errors.New("tgclient: nil params")
	}
	var msg *telego.Message
	err := c.runWrite(ctx, params.ChatID.ID, "sendDocument",
		func(ctx context.Context) error {
			m, e := c.bot.SendDocument(ctx, params)
			if e != nil {
				return e
			}
			msg = m
			return nil
		},
		func(newSigned int64) { params.ChatID = telego.ChatID{ID: newSigned} },
	)
	return msg, err
}

// EditMessageText wraps telego.Bot.EditMessageText.
func (c *Client) EditMessageText(ctx context.Context, params *telego.EditMessageTextParams) (*telego.Message, error) {
	if params == nil {
		return nil, errors.New("tgclient: nil params")
	}
	var msg *telego.Message
	err := c.runWrite(ctx, params.ChatID.ID, "editMessageText",
		func(ctx context.Context) error {
			m, e := c.bot.EditMessageText(ctx, params)
			if e != nil {
				return e
			}
			msg = m
			return nil
		},
		func(newSigned int64) { params.ChatID = telego.ChatID{ID: newSigned} },
	)
	return msg, err
}

// DeleteMessage wraps telego.Bot.DeleteMessage.
func (c *Client) DeleteMessage(ctx context.Context, params *telego.DeleteMessageParams) error {
	if params == nil {
		return errors.New("tgclient: nil params")
	}
	return c.runWrite(ctx, params.ChatID.ID, "deleteMessage",
		func(ctx context.Context) error { return c.bot.DeleteMessage(ctx, params) },
		func(newSigned int64) { params.ChatID = telego.ChatID{ID: newSigned} },
	)
}

// BanChatMember wraps telego.Bot.BanChatMember.
func (c *Client) BanChatMember(ctx context.Context, params *telego.BanChatMemberParams) error {
	if params == nil {
		return errors.New("tgclient: nil params")
	}
	return c.runWrite(ctx, params.ChatID.ID, "banChatMember",
		func(ctx context.Context) error { return c.bot.BanChatMember(ctx, params) },
		func(newSigned int64) { params.ChatID = telego.ChatID{ID: newSigned} },
	)
}

// UnbanChatMember wraps telego.Bot.UnbanChatMember.
func (c *Client) UnbanChatMember(ctx context.Context, params *telego.UnbanChatMemberParams) error {
	if params == nil {
		return errors.New("tgclient: nil params")
	}
	return c.runWrite(ctx, params.ChatID.ID, "unbanChatMember",
		func(ctx context.Context) error { return c.bot.UnbanChatMember(ctx, params) },
		func(newSigned int64) { params.ChatID = telego.ChatID{ID: newSigned} },
	)
}

// RestrictChatMember wraps telego.Bot.RestrictChatMember.
func (c *Client) RestrictChatMember(ctx context.Context, params *telego.RestrictChatMemberParams) error {
	if params == nil {
		return errors.New("tgclient: nil params")
	}
	return c.runWrite(ctx, params.ChatID.ID, "restrictChatMember",
		func(ctx context.Context) error { return c.bot.RestrictChatMember(ctx, params) },
		func(newSigned int64) { params.ChatID = telego.ChatID{ID: newSigned} },
	)
}

// AnswerCallbackQuery is rate-limited by the originating chat when the
// callback came from a chat message. We do not attempt to thread the
// chat id from the callback context here - the wrapper surface is
// keyed by chat id only, so callback answers go through the retry/
// migration layers without per-chat rate limiting.
//
// Callback queries also have a strict ~10s server-side timeout, so
// adding queueing latency would just cause "query is too old" errors;
// retry alone is the right safety net.
func (c *Client) AnswerCallbackQuery(ctx context.Context, params *telego.AnswerCallbackQueryParams) error {
	if params == nil {
		return errors.New("tgclient: nil params")
	}
	return retry.Do(ctx, c.retryPolicy, func(ctx context.Context) error {
		return c.bot.AnswerCallbackQuery(ctx, params)
	})
}

// AnswerInlineQuery has the same characteristics as callback answers
// (no chat id, strict server timeout) - retry only.
func (c *Client) AnswerInlineQuery(ctx context.Context, params *telego.AnswerInlineQueryParams) error {
	if params == nil {
		return errors.New("tgclient: nil params")
	}
	return retry.Do(ctx, c.retryPolicy, func(ctx context.Context) error {
		return c.bot.AnswerInlineQuery(ctx, params)
	})
}

// Read-only passthrough methods. These do not produce chat traffic so
// they bypass the rate limiter; we still apply retry because GetChat /
// GetChatMember can hit transient 5xx during Telegram outages.

// GetMe returns bot identity.
func (c *Client) GetMe(ctx context.Context) (*telego.User, error) {
	var u *telego.User
	err := retry.Do(ctx, c.retryPolicy, func(ctx context.Context) error {
		got, e := c.bot.GetMe(ctx)
		if e != nil {
			return e
		}
		u = got
		return nil
	})
	return u, err
}

// GetChat fetches chat metadata.
func (c *Client) GetChat(ctx context.Context, params *telego.GetChatParams) (*telego.ChatFullInfo, error) {
	if params == nil {
		return nil, errors.New("tgclient: nil params")
	}
	var info *telego.ChatFullInfo
	err := retry.Do(ctx, c.retryPolicy, func(ctx context.Context) error {
		got, e := c.bot.GetChat(ctx, params)
		if e != nil {
			return e
		}
		info = got
		return nil
	})
	return info, err
}

// GetChatMember returns the membership status of a single user.
func (c *Client) GetChatMember(ctx context.Context, params *telego.GetChatMemberParams) (telego.ChatMember, error) {
	if params == nil {
		return nil, errors.New("tgclient: nil params")
	}
	var m telego.ChatMember
	err := retry.Do(ctx, c.retryPolicy, func(ctx context.Context) error {
		got, e := c.bot.GetChatMember(ctx, params)
		if e != nil {
			return e
		}
		m = got
		return nil
	})
	return m, err
}

// GetChatAdministrators is used by the AdminCache; rate-limit bypass
// is safe because the cache amortizes calls to once per minute per chat.
func (c *Client) GetChatAdministrators(ctx context.Context, params *telego.GetChatAdministratorsParams) ([]telego.ChatMember, error) {
	if params == nil {
		return nil, errors.New("tgclient: nil params")
	}
	var ms []telego.ChatMember
	err := retry.Do(ctx, c.retryPolicy, func(ctx context.Context) error {
		got, e := c.bot.GetChatAdministrators(ctx, params)
		if e != nil {
			return e
		}
		ms = got
		return nil
	})
	return ms, err
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// Compile-time guarantee that Client satisfies the existing TelegramAPI
// interface so it is a drop-in replacement.
var _ shared.TelegramAPI = (*Client)(nil)
