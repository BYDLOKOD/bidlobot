---
id: architecture
kind: spec
touches:
  - cmd/bidlobot/
  - internal/bot/
  - internal/domain/
  - internal/games/
  - internal/shared/
  - internal/storage/
  - internal/testutil/
  - internal/text/
written: 2026-05-14
updated: 2026-05-15
---

# Architecture

How the binary is composed and where the moving parts live. Read after
`PRD.md` and `50_telegram.md`, before changing wiring. Revised
2026-08-11 for the post-moderation content-tools era (DM console
removed in caa8f55).

## Layout

```
cmd/
  bidlobot/        production entrypoint; wires everything below
  bidlobot-backup/ online bbolt backup (db.View + WriteTo)
  probe/           one-shot getMe; verifies BotFather config

internal/
  bot/
    routes.go          registerRoutes: middleware order + command routes
    app.go             App wiring, background goroutines, shutdown
    setup.go           setMyCommands scopes
    membership.go      membership observers + admission gate + onboarding
    captcha.go         new-member captcha wiring (opt-in)
    deferred.go        /flush per-user retry queue
    summarize.go       /summarize + /итог handler
    youtube_sanitizer.go  YT si= strip (delete+repost)
    tiktok_repost.go      TikTok video repost (yt-dlp)
    xpost.go              X/Twitter sidecar client
    referral.go           referral catalog UX
    reputation.go         praise/roast/rep economy
    games*.go             game registry + handlers (dice/battle/quiz/...)
    inline.go             read-only inline catalog (stats/games/help)
    cooldown.go           per-user per-command flood gate
    callback.go           legacy v1: dispatcher catch-all
    failure_catalog.go    randomized Russian decline phrases
  domain/
    membership/  Member + Chat upserts; activity truth source
    stats/       lifetime counters + daily + Russian display
    monthstats/  per-calendar-month nominations engine
    moderation/  warning store (legacy, no surface feeds it)
    cleanup/     inactive query + ban+unban kicker (drives gracekick)
    gracekick/   daily tag->grace->kick lifecycle (idle: no seeding cmd)
    pending/     TTL-bound action store (legacy)
    captcha/     math challenge lifecycle + welcome animation
    summarize/   RAM ring buffer + OMP/Pi runner
    referral/    referral matching/validation
    reputation/  balance economy rules
  games/           dice / battle / duel / guess / hangman / quiz ...
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
 membership   stats svc    summarize
 observers    (read)       svc (Pi)
     ^            ^            ^
     |            |            |
+----+------------+------------+--------------------------+
|              internal/bot routing layer                 |
|                                                         |
|  PRIVATE (privatePredicate):                            |
|   /help /start (same text) + /chats (owner console,     |
|   revoke flow)                                          |
|                                                         |
|  PUBLIC (supergroupPredicate):                          |
|   games routes (slash + callbacks)                      |
|   passive observers, in order:                          |
|     membership -> stats count -> summarize recorder     |
|   content middlewares, in order:                        |
|     youtubeSanitizer -> tiktokReposter -> xpostReposter |
|   cooldown-gated commands: /stats /flush /help          |
|     /summarize+/итог /refs /refreg /refreport           |
|   InlineService -> read-only catalog only               |
|                                                         |
|  fanouts:                                               |
|   message_reaction -> battle observer + membership      |
|   my_chat_member  -> admission gate + onboarding        |
|   chat_member     -> membership + captcha               |
|  callbacks (order matters):                             |
|   captcha (cap:) -> referral (rf:) -> owner chats (oc:) |
|     -> v1: dispatcher catch-all                         |
+---------------------------------------------------------+
                            ^
                   telegohandler.BotHandler
                            ^
                   long-poll / GetUpdates
```

Middleware order in `routes.go:41-149` (root `loggingHandler`
first): `privateGroup` (/help /start /chats) and `sgGroup`
(supergroup). `sgGroup.Use` chain = `membershipMessageMiddleware` ->
`statsCountHandler` -> `summarizeRecorder` (if wired) ->
`youtubeSanitizer` -> `tiktokReposter` -> `xpostReposter`. The
observers MUST see the original human message before the sanitizer
deletes+reposts it. Game routes register before the observers so their
slash commands and callbacks coexist.

App-level middleware in `Run`: `healthMiddleware` (update freshness)
then `inFlightMiddleware` (per-update WaitGroup).

Side-effect axes:
- Every chat-visible send flows through `tgclient.Client` (per-chat
  rate limit -> retry -> migration): stats, games, reputation,
  referrals, summarize placeholder/edit, sanitizer/reposter sends,
  captcha mute/kick/welcome, onboarding. This keeps high-volume public
  paths inside Telegram's 20 msg/min/chat budget under load.
- Heavy media work (TikTok download/upload, xpost screenshot/videos,
  captcha welcome animation) runs **fire-and-forget**
  (`go f(context.Background(), ...)`) because the update loop is
  sequential and a synchronous multi-hundred-KB upload would stall
  every member's update. These are deliberately NOT in `App.inFlight`
  (best-effort; shutdown may lose one). Summarize IS tracked (app
  context + deadline, must finish inside the shutdown budget).
- The legacy `v1:` callback dispatcher still uses the raw bot; it is
  per-tap, low volume, and no surface feeds it destructive pendings
  (nothing creates them anymore - dead surface, kept for the game
  callbacks that share the catch-all).

## bbolt schema (`storage/bolt.go`, 28 buckets)

| Bucket | Key | Value |
|--------|-----|-------|
| `profiles`, `profiles_by_chat` | n/a | empty placeholders (archived bio domain) |
| `members` | `m:<userID>:<absChatID>` | `membership.Member` JSON |
| `members_by_chat` | `mc:<absChatID>:<userID>` | secondary index |
| `chats` | `c:<absChatID>` | `membership.Chat` JSON |
| `stats` | `s:<userID>:<absChatID>` | `stats.Stats` JSON |
| `stats_by_chat` | `sc:<absChatID>:<userID>` | secondary index |
| `stats_daily` | `d:<absChatID>:<YYYY-MM-DD>` | per-day counters (MSK day) |
| `stats_month` / `stats_month_idx` | month counters | per-(chat, "YYYY-MM", user) |
| `stats_month_state` / `stats_month_summary` | per-chat | import HWM, sealed months, memoized HTML |
| `stats_month_imported_ids` | per-chat | dedupe set (import path) |
| `warnings` | `w:{uuid}` | `moderation.Warning` JSON (legacy) |
| `warns_by_target` | `wt:<absChatID>:<targetUserID>:<uuid>` | secondary index |
| `pending_actions` | `pa:<16-hex>` | `pending.Action` JSON (legacy) |
| `dice_leaderboard`, `quiz_leaderboard` | per-chat | game leaderboards |
| `reputation` | `r:<absChatID>:<userID>` | `{balance}` |
| `captcha` | `cc:<id>` | challenge |
| `captcha_user_idx` | `ccu:<absChatID>:<userID>` | active challenge lookup |
| `gracekick` | per-chat | campaign records (idle) |
| `referral_services` | `rfs:<absChatID>:<svcID>` | service |
| `referral_services_name_idx` | `rfsn:<absChatID>:<name>` | name -> service ID |
| `referrals` | `rf:<absChatID>:<refID>` | referral |
| `admission_attempts` | `aa:<userID>` | 8-byte BE counter |
| `deferred_jobs` | `dj:<zero-padded UnixNano>` | JSON `DeferredJob` (FIFO) |

`dm_sessions` was removed with the DM console (caa8f55). Buckets
created idempotently in `storage.NewBoltStore`. `MigrateChatID`
rekeys stats/members/chats/warnings/monthstats/dailystats/referrals;
**known gap**: reputation, captcha, deferred_jobs, admission_attempts,
gracekick and game leaderboards are NOT rekeyed on group->supergroup
migration (documented, not silently handled).

## Key invariants

1. **Membership is bottom-up.** No `getChatMembers`; the bot learns a
   user only from an observed event. No import path anymore (removed
   caa8f55).
2. **Reactions count as activity.** `LastMessageAt OR LastReactionAt`.
3. **No moderation surface.** No public or DM moderation commands
   exist; the legacy `v1:` dispatcher is a dead path. Do not
   reintroduce public moderation, advertise it in
   `helpSupergroup`/`setCommands` group scope, or route destructive
   actions through inline (inline posts publicly).
4. **Content middlewares are repost-then-delete (YT, TikTok) or
   add-only (xpost)**, never delete-first. A failed repost leaves the
   original intact.
5. **No third-party pings.** No user-triggered command emits
   `@handle` / `tg://user?id=` / `text_mention` for a third party;
   attribution headers render inert display names.
6. **Owner-gated installation.** Non-owner bot adds trigger immediate
   LeaveChat + owner DM notice (bounded to 2 per actor).
7. **Cooldown is self-evicting.** Per-(user,command) gate sweeps
   entries older than 10m every 5m - bounded memory.
8. **Workers respect app context.** Gracekick tick, deferred GC,
   pending GC, captcha sweep, summarize take App's signal context;
   `App.Stop()` waits `inFlight` up to `ShutdownTimeout` (10s).
9. **Heavy sends never block the loop.** Fire-and-forget
   `context.Background()` goroutines for media uploads/downloads.
10. **Privacy + admin guard inputs.** Without `setprivacy: disabled`
    the sanitizer/reposters/summarize recorder never see message
    content; `cmd/probe` reports the flag.

## Subscription set

`AllowedUpdates` (`app.go:116-125`): `message`, `callback_query`,
`my_chat_member` (admission + onboarding), `chat_member` (membership +
captcha), `message_reaction` (membership + battle), `inline_query`.
No `edited_message`, no `chat_join_request`.

## Background goroutines (`App.Run`)

| Goroutine | Period | Notes |
|-----------|--------|-------|
| stats buffer flush | 60s | lifetime counters |
| month buffer flush | 60s | monthly nominations |
| pending GC | 1 min | legacy TTL store |
| deferred GC | 10 min | 48h TTL jobs |
| daily gracekick tick | `CLEANUP_DAILY_AT` UTC | idle (no seeding command) |
| captcha sweep | min(timeout/3, 30s), >=5s | kicks unanswered challenges |
| health server | - | /health + /version |

## Failure handling

| Failure | Where | Response |
|---------|-------|----------|
| 429 | `retry.Do` | sleep `retry_after`+jitter, retry once |
| 5xx | `retry.Do` | 1/2/4/8s backoff, 4 attempts |
| other 4xx | `retry.Do` | surface to caller |
| `migrate_to_chat_id` | `tgclient` | `MigrateChatID` + replay |
| bbolt I/O | service | propagate; reply "временная ошибка" |
| TikTok download/audio fail | `tiktok_repost.go` | enqueue to deferred queue; original kept |
| TikTok too-large/send fail | `tiktok_repost.go` | public decline note; original kept |
| xpost any failure | `xpost.go` | decline note; original kept (never deleted) |
| YT sanitizer repost fail | `youtube_sanitizer.go` | original left intact |
| summarize provider fail | `summarize.go` | enqueue to deferred queue; placeholder stays |
| summarize timeout/budget | `summarize.go` | Russian error, lower N |
| Game/stats flood | `cooldown.gateMsg` | silent drop (bounded notice) |
| Per-chat rate burst | ratelimit queue | drop oldest + WARN |
| Non-owner bot add | `membership.go` | LeaveChat + owner DM notice |
| Health DB/updates/GetMe | `/health` 503 | reason in body |
| SIGINT/SIGTERM | `signal.NotifyContext` | cancel -> `App.Stop()` -> flush -> `db.Close()` |

## Deferred queue

`internal/bot/deferred.go` + `storage/deferred_repo.go`. Per-user
retry queue for failed **TikTok exports** and **summarize** calls
(commits 81f45c9, 47cdd99). Public `/flush` (30s cooldown) processes
the caller's own jobs sequentially in a background goroutine: tiktok
-> `tryTikTokExport` (full download->validate->upload->delete cycle;
error keeps the job), summarize -> `retrySummarize` (re-runs
`Summarize`, edits the placeholder with 25s timeout). Success ->
`Delete(key)`. Storage: `deferred_jobs`, key `dj:<zero-padded
UnixNano>` (lexicographic FIFO), JSON `DeferredJob{user_id, type,
chat_id, message_id, payload, created_at}`, TTL 48h, GC every 10 min.
Always wired; nil queue (tests) -> failures fall back to decline
replies.

## Where to add a feature

| Want | Touch |
|------|-------|
| New public command | `routes.go` (or `registerGameRoutes`); gate with `a.gateMsg` if floodable; add to `setup.go` scopes + `helpSupergroup` |
| New passive content middleware | `routes.go` sgGroup.Use chain, after the observers; repost-then-delete or add-only discipline |
| New persistent entity | `storage/<e>_repo.go` + bucket in `bolt.go` + domain service |
| New background sweep | `app.go` Run() goroutine; share `App.inFlight` if it must block shutdown |
| New /health probe | `health.go` healthChecker |
| New callback family | `routes.go` BEFORE the `v1:` catch-all; narrow predicate first |
