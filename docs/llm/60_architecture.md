---
id: architecture
kind: spec
---

# Architecture

How the binary is composed and where the moving parts live. Read after
`10_scope.md` and `50_telegram.md`, before changing wiring. Revised
2026-05-15 for the privacy rework (DM console).

## Layout

```
cmd/
  bidlobot/        production entrypoint; wires everything below
  bidlobot-backup/ online bbolt backup (db.View + WriteTo)
  bidlobot-probe/  one-shot getMe; verifies BotFather config

internal/
  histimport/      streams a Telegram Desktop chat export (with .gz/.zip
                   decompress) into the members bucket + monthly stats;
                   driven in-process by DM /import (see 35_history_import)
  bot/
    dm_console*.go    private moderation console (the ONLY mod surface)
    dm_text.go        DM-console Russian copy
    routes.go         registerRoutes + redirectModerationToDM
    inline.go         read-only catalog (stats/games/help) - NO moderation
    cooldown.go       per-user per-command flood gate
    callback.go       legacy public dispatcher (no longer fed mod pendings)
    membership.go     event handlers + onboarding message
  domain/
    membership/  Member + Chat upserts; cleanup truth source
    stats/       counter buffer + Russian display formatting
    moderation/  warn/mute/ban service; warning store
    cleanup/     inactive query + kick worker
    pending/     TTL-bound action store (ban + cleanup confirm)
    dmsession/   admin -> selected target chat
  shared/          admin cache, target resolve, format
  shared/ratelimit per-chat outgoing token bucket
  shared/retry     429+5xx retry policy
  shared/tgclient  composed wrapper: migration -> retry -> rate-limit
  storage/         bbolt repos + key conventions + migration
  testutil/        MockAPI + recorder + update factories
  text/            user-facing Russian strings (single source)
```

## Call graph

```
   Telegram Bot API
          ^
+-----------------------------+
|  shared/tgclient.Client     |  (migration -> retry -> ratelimit)
+-----------------------------+
     ^            ^            ^
 moderation    cleanup      stats svc
  Service      Service      (read only)
     ^            ^            ^
     |            |            |
+----+------------+------------+--------------------------+
|              internal/bot routing layer                 |
|                                                         |
|  PRIVATE (dmCallbackPredicate / privatePredicate):      |
|   DMConsole.HandleMessage  -> mod/cleanup/stats svc      |
|   DMConsole.HandleCallback -> pick / apply / cancel /    |
|                               abort (actor-locked)       |
|                                                         |
|  PUBLIC (supergroupPredicate):                          |
|   stats handler + games (cooldown-gated)                |
|   moderation verb -> redirectModerationToDM (delete +    |
|                       DM the admin; never executes)      |
|   InlineService -> read-only catalog only                |
|   middleware: membership, stats counter, in-flight,      |
|               health, logging                            |
|                                                         |
|  legacy CallbackDispatcher (v1:) still wired but no      |
|  surface creates destructive pendings for it             |
+---------------------------------------------------------+
                            ^
                   telegohandler.BotHandler
                            ^
                   long-poll / GetUpdates
```

Side-effect axes:
- Every chat-visible send flows through `tgclient.Client` (per-chat
  rate limit -> retry -> migration): moderation/cleanup, games
  (dice/battle/quiz), `/stats` replies, help, onboarding, and the
  public-moderation redirect (`App.sender`, injected via
  `AttachSender`; games via the `GamesSender` union; stats via
  `MessageSender`). This keeps the highest-volume public path inside
  Telegram's 20 msg/min/chat budget under load.
- Two deliberate raw-`*telego.Bot` exceptions: the DM console
  (`dmSender`; single-admin, low volume, recording-fake testable) and
  `inline_query` answers (`AnswerInlineQuery` has a strict server
  timeout and no chat id, so it is retry-only by design - queueing it
  would just cause "query is too old"). The legacy `v1:` callback
  dispatcher also still uses the raw bot; it is per-tap, low volume,
  and no surface feeds it destructive pendings post-rework.
- Storage writes go through bbolt repos.

## bbolt schema

| Bucket | Key | Value |
|--------|-----|-------|
| `profiles`, `profiles_by_chat` | n/a | empty placeholders (archived bio domain) |
| `members` | `m:<userID>:<absChatID>` | `membership.Member` JSON |
| `members_by_chat` | `mc:<absChatID>:<userID>` | secondary index |
| `chats` | `c:<absChatID>` | `membership.Chat` JSON |
| `stats` | `s:<userID>:<absChatID>` | `stats.Stats` JSON |
| `stats_by_chat` | `sc:<absChatID>:<userID>` | secondary index |
| `warnings` | `w:<uuid>` | `moderation.Warning` JSON |
| `warns_by_target` | `wt:<absChatID>:<targetUserID>:<uuid>` | secondary index |
| `pending_actions` | `pa:<16-hex>` | `pending.Action` JSON; swept past `ExpiresAt` |
| `dm_sessions` | `dm:<adminUserID>` | `dmsession.Session` (selected chat) |

Buckets created idempotently in `storage.NewBoltStore` (the in-process
`/import` path reuses the same store, so no separate bucket
bootstrap). Migration on `migrate_to_chat_id` rewrites the substantive
buckets in one tx.

## Key invariants

1. **Membership is bottom-up.** No `getChatMembers`; the bot learns a
   user only from an observed event - or from a DM `/import` seeding
   history. Cleanup previews always state the observation window.
2. **Reactions count as activity.** `LastMessageAt OR LastReactionAt`
   vs cutoff; a react-only lurker is preserved.
3. **Moderation is DM-only.** No public slash; inline carries no
   moderation; the group command menu advertises none. The public
   timeline never carries bot-initiated moderation.
4. **DM destructive safety.** Ban + cleanup: pending TTL 5m,
   actor-lock, admin re-check at confirm, delete-before-execute (no
   replay). DM is single-actor so no chat-pin needed.
5. **Cleanup Stop is race-free.** `cleanupRuns.start` registers the
   cancel func + claims the chat BEFORE the Stop button renders; a
   second admin's concurrent cleanup on the same chat is refused.
6. **Cooldown is self-evicting.** Per-(user,command) gate sweeps
   entries older than 10m every 5m - bounded memory.
7. **Workers respect app context.** Cleanup worker + stats flush +
   pending GC take App's signal context; `App.Stop()` waits `inFlight`
   up to `ShutdownTimeout` (10s).
8. **Privacy + admin guard inputs.** Without `setprivacy: disabled` the
   bot sees only commands/@-mentions; `cmd/bidlobot-probe` reports it.

## Subscription set

`AllowedUpdates`: `message`, `callback_query`, `my_chat_member`
(chat upsert + onboarding message), `chat_member` (status + admin cache
invalidation), `message_reaction` (LastReactionAt), `inline_query`.

## Failure handling

| Failure | Where | Response |
|---------|-------|----------|
| 429 | `retry.Do` | sleep `retry_after`+jitter, retry once |
| 5xx | `retry.Do` | 1/2/4/8s backoff, 4 attempts |
| other 4xx | `retry.Do` | surface to caller |
| `migrate_to_chat_id` | `tgclient` | `MigrateChatID` + replay |
| bbolt I/O | service | propagate; reply "временная ошибка" |
| Pending expired | DM console | toast "истекло или уже выполнено" |
| Wrong actor on confirm | DM console | toast "только инициатор" |
| Admin demoted mid-flight | DM console | toast "больше не админ" + clear session |
| Public moderation attempt | `redirectModerationToDM` | delete command + DM the admin |
| Game/stats flood | `cooldown.gateMsg` | silent drop (no added spam) |
| Per-chat rate burst | ratelimit queue | drop oldest + WARN |
| Health DB/updates/GetMe | `/health` 503 | reason in body |
| SIGINT/SIGTERM | `signal.NotifyContext` | cancel -> `App.Stop()` -> flush -> `db.Close()` |

## Where to add a feature

| Want | Touch |
|------|-------|
| New DM admin command | `dm_console.go` HandleMessage switch + service |
| New public read/game command | `routes.go` + handler; gate with `a.gateMsg` if floodable |
| New persistent entity | `storage/<e>_repo.go` + bucket in `bolt.go` + domain service (the in-process `/import` path reuses the same store) |
| New background sweep | `app.go` Run() goroutine, share `App.inFlight` if it must block shutdown |
| New /health probe | `health.go` healthChecker |

> Do NOT reintroduce public moderation, advertise it in
> `helpSupergroup`/`setCommands` group scope, or route a destructive
> action through inline (inline posts publicly - it is not private).
