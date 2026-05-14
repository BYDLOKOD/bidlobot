---
id: architecture
kind: spec
---

# Architecture

How the binary is composed, which side effects each layer owns, and where the moving parts live. Read after `10_scope.md` and `50_telegram.md`, before changing any wiring.

## Layout

```
cmd/
  bidlobot/        production entrypoint; wires everything below
  bidlobot-backup/ online bbolt backup (db.View + WriteTo)
  probe/           one-shot getMe; verifies BotFather config
  smoke/           bounded production wiring against real chat (INTEGRATION_TEST=1)

internal/
  bot/             telego dispatch + middleware + dispatcher + executors
  domain/
    membership/    Member + Chat upserts; the cleanup truth source
    stats/         counter buffer + display formatting
    moderation/    warn/mute/ban service; warning store
    cleanup/       inactive query + kick worker
    pending/       TTL-bound action store for two-step inline confirms
  shared/          cross-domain helpers (admin cache, target resolve, format)
  shared/ratelimit token-bucket per-chat outgoing limiter
  shared/retry     429+5xx retry policy
  shared/tgclient  composed wrapper: migration -> retry -> rate-limit
  storage/         bbolt repos + key conventions + migration
  testutil/        MockAPI + recorder + update factories
  text/            user-facing Russian strings (single source)
```

## Layered call graph

```
   Telegram Bot API
          ^
          |
+-----------------------------+
|     internal/shared/        |
|     tgclient.Client         |  (migration -> retry -> ratelimit)
+-----------------------------+
          ^
          |
  +---------------+   +-----------------+   +----------------+
  |  moderation   |   |     cleanup     |   |   stats svc    |
  |    Service    |   |     Service     |   |   (read only)  |
  +---------------+   +-----------------+   +----------------+
          ^                    ^                    ^
          |                    |                    |
  +-------+-------+    +-------+--------+    +------+--------+
  |  Moderation   |    |   Cleanup      |    |   stats       |
  |  Executor     |    |   Executor     |    |   Handler     |
  +---------------+    +----------------+    +---------------+
          ^                    ^                    ^
          |                    |                    |
   +------+--------------------+--------------------+------+
   |             internal/bot routing layer                 |
   |  CallbackDispatcher (validates verb, TTL, actor,       |
   |  admin) -> executor; InlineService (builds previews,   |
   |  writes pending) -> answerInlineQuery; middleware      |
   |  (membership, stats counter, in-flight, health)        |
   +-------------------------------------------------------+
                            ^
                            |
                  telegohandler.BotHandler
                            ^
                            |
                   long-poll / GetUpdates
```

Two side-effect axes:
- **Outgoing API calls** flow through `tgclient.Client` for moderation/cleanup; the dispatcher and inline service still talk to `*telego.Bot` directly for `AnswerCallbackQuery` / `AnswerInlineQuery` / `EditMessageText` because the wrapper bypasses rate-limiting on those endpoints anyway (Telegram enforces strict server-side timeouts).
- **Storage writes** all go through bbolt repos; only `cleanup` and `moderation` execute kicks/restricts.

## bbolt schema

| Bucket | Key format | Stored value |
|--------|-----------|--------------|
| `profiles`, `profiles_by_chat` | n/a | empty placeholders for the archived bio domain (see `archive/profiles-bio`); kept so a future revival doesn't need a schema migration |
| `members` | `m:<userID>:<absChatID>` (20-digit zero-padded) | `membership.Member` JSON |
| `members_by_chat` | `mc:<absChatID>:<userID>` | empty (secondary index) |
| `chats` | `c:<absChatID>` | `membership.Chat` JSON |
| `stats` | `s:<userID>:<absChatID>` | `stats.Stats` JSON |
| `stats_by_chat` | `sc:<absChatID>:<userID>` | empty (secondary index) |
| `warnings` | `w:<uuid>` | `moderation.Warning` JSON |
| `warns_by_target` | `wt:<absChatID>:<targetUserID>:<uuid>` | empty (secondary index) |
| `pending_actions` | `pa:<16-hex>` | `pending.Action` JSON; sweeper deletes past `ExpiresAt` |

Buckets created idempotently in `storage.NewBoltStore`. Migration on `migrate_to_chat_id` rewrites all 8 substantive buckets in a single tx via `storage.MigrateChatID`.

## Key invariants

1. **Membership is bottom-up.** Bot API has no `getChatMembers`; the bot only learns about a user from an observed event (message, reaction, chat_member). All cleanup logic must respect that the candidate set is "people we have seen", not "everyone in the chat" - every preview message documents the observation window.

2. **Reactions count as activity.** The cleanup query checks `LastMessageAt OR LastReactionAt` against the cutoff. A "lurker" who never writes but reacts is preserved.

3. **Forward-attack guard.** Pending actions carry `ActorUserID`; the dispatcher refuses to execute when `query.From.ID != action.ActorUserID`. Forwarded inline-keyboard messages cannot be exploited to apply someone else's pending elsewhere.

4. **Apply is single-shot.** The dispatcher deletes the pending row and strips the keyboard after `cbApply`, so a fast double-tap or a stale message refresh cannot replay the action. `cbPreview` keeps the row because the subsequent `cbApply` still needs it.

5. **Admin re-check at confirm.** The admin set may change in the 5-minute pending TTL window; the dispatcher hits `AdminCache.IsAdmin` again before invoking the executor.

6. **Workers respect app context.** Background goroutines (cleanup kick worker, stats flush, pending GC) take the App's signal-aware context. `App.Stop()` waits up to `ShutdownTimeout` (10s) on `inFlight` for cleanup workers + handler middleware.

7. **Privacy + admin guard the inputs.** Without `setprivacy: disabled` the bot sees only commands and @-mentions; without `setinline: enabled` the inline carousel never appears. Both are required for production; `cmd/probe` reports both.

## Subscription set

`AllowedUpdates` in `bot.App.Run`:

- `message` - stats counter (middleware), membership upsert, command routing
- `callback_query` - dispatcher
- `my_chat_member` - chats bucket upsert + admin-rights warnings
- `chat_member` - membership status updates + admin cache invalidation
- `message_reaction` - membership LastReactionAt (requires bot admin)
- `inline_query` - InlineService

Anything else gets dropped at the long-poll boundary, so adding a feature usually means: extend `AllowedUpdates`, add a handler in `routes.go`, and (if it touches state) add a domain service.

## Failure handling

| Failure | Where caught | Response |
|---------|--------------|----------|
| Telegram 429 | `retry.Do` | sleep `retry_after`+jitter, retry once |
| Telegram 5xx | `retry.Do` | exponential backoff (1/2/4/8s+jitter), 4 attempts |
| Telegram 4xx other | `retry.Do` | surface immediately to caller |
| `migrate_to_chat_id` | `tgclient.Client.runWrite` | `storage.MigrateChatID` rewrites bbolt + retry call with new chat_id |
| bbolt I/O failure | service layer | propagate up; handler logs WARN and replies "временная ошибка" |
| Pending TTL expired | dispatcher | toast + edit message "Действие истекло" |
| Wrong actor on apply | dispatcher | toast "Только инициатор..." |
| Admin demoted between preview and apply | dispatcher | toast "У вас нет прав..." |
| Per-chat rate burst | ratelimit queue (FIFO, cap 50, drop oldest) | drop with WARN log; chat sees no message |
| Health: DB closed | `/health` 503 | `{"reason":"db_closed"}` |
| Health: stale updates (> 5 min) | `/health` 503 | `{"reason":"no_updates"}` |
| Health: GetMe failure | `/health` 503 | `{"reason":"getme_failed"}` |
| SIGINT / SIGTERM | `signal.NotifyContext` | cancel root context -> long-poll halts -> `App.Stop()` waits `inFlight` up to 10s -> stats flush -> health stop -> `db.Close()` |

## What's intentionally NOT wrapped

The Phase 5 wrapper is selective:
- Read endpoints (`GetMe`, `GetChat`, `GetChatMember`, `GetChatAdministrators`) skip rate-limit but go through retry - they're idempotent and need to be cheap.
- `AnswerCallbackQuery` / `AnswerInlineQuery` skip rate-limit because Telegram caps them at ~10s server-side; queueing past that returns "query is too old".
- Help / stats reply paths in handlers still call `ctx.Bot()` directly; rates are 1-per-command and don't justify the refactor cost. Documented as known limitation; can be lifted by passing `tgclient.Client` through to `routes.registerRoutes`.

## Where to add a new feature

| Want to add... | Touch this |
|--------------|-----------|
| New bot command (slash) | `internal/bot/routes.go` + new handler in `internal/bot/<feature>.go` |
| New inline command | `internal/bot/inline.go` `BuildResults` switch + `internal/bot/executors_<feature>.go` for the callback executor |
| New persistent entity | `internal/storage/<entity>_repo.go` + bucket in `internal/storage/bolt.go` + domain service in `internal/domain/<entity>/` |
| New background sweep | `internal/bot/app.go` Run() goroutine, share App.inFlight if it should block shutdown |
| New /health probe | `internal/bot/health.go` healthChecker fields + checks |
