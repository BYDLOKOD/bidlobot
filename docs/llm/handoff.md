---
id: handoff
kind: guide
---

# Handoff: next session action plan

Last updated: 2026-05-14, after Phases 0-3, 5, and most of 6. Phase 4 (mini-games) currently in progress in a parallel agent worktree.

## Current state

Branch: `master`.

Six implementation phases planned:
- **Phase 0 done** - archive bio, clean docs
- **Phase 1 done** - membership tracking (chats bucket, member upserts on message/reaction/chat_member); stats now prints real names; moderation refuses to act on unknown @usernames
- **Phase 2 done** - inline mode as a slash-command launcher (read-only commands only); destructive deferred to Phase 3
- **Phase 3** - cleanup feature + pending-actions storage + destructive inline confirms
- **Phase 4** - mini-games (dice, reaction-battle, code-quiz)
- **Phase 5 done** - production-readiness (rate limiter, retry, migration, /health, graceful shutdown, backup, CI, config validation)
- **Phase 6** - integration testing (replayer + smoke tests)

## BotFather one-time setup

Verified 2026-05-14 against the live token: `can_read_all_group_messages=false`, `supports_inline_queries=false`. Both must be flipped before any of Phase 1-3 functions in real chats. Run, in @BotFather:

1. `/setprivacy` -> bot -> Disable. **After this you must remove and re-add the bot to the chat** (privacy is cached at join time).
2. `/setinline` -> bot -> placeholder text such as `stats top, cleanup 6mo, warn @user`.
3. `/setinlinefeedback` -> Disabled (we don't process chosen_inline_result yet).
4. Confirm with `go run ./cmd/probe` -- expects `can_read_all=true, supports_inline=true`.

## Validation tools

- `cmd/probe` -- one-shot getMe, no polling, no side effects. Use to verify token + BotFather config.
- `cmd/smoke` -- runs the full production wiring (rate limiter + retry + migration handler + dispatcher + executors) against the real chat for a bounded duration. Refuses to start without `INTEGRATION_TEST=1`. `SMOKE_TIMEOUT` env (default 60) sets the auto-shutdown. Watch the JSON log to see each handler firing.
- `internal/bot/end_to_end_test.go` -- in-process integration tests covering inline -> dispatcher -> executor -> bbolt for warn / ban / cleanup. No live API, no BotFather requirement.
- `internal/bot/replay_test.go` -- offline integration tests that stream `testdata/session*.jsonl` recordings through the membership/cleanup domain.

## Manual smoke checklist

Run after BotFather is fixed and the bot has been re-added to the chat:

```
INTEGRATION_TEST=1 SMOKE_TIMEOUT=300 go run ./cmd/smoke
```

Then in the chat, in this order:

1. **Bot identity** - JSON log on startup must show `can_read_all=true, supports_inline=true`.
2. **Help** - `/help` -> bot replies with the help block.
3. **Stats baseline** - `/stats` -> shows current chat overview. Likely 0 entries on a fresh DB.
4. **Stats grows** - write 3-5 plain messages. After a few seconds (within 60s flush window or immediately via merged buffer read) `/stats top` lists you with the right count.
5. **Reaction tracking** - react with any emoji to a recent message. Membership's `LastReactionAt` for your user_id updates. (Verify by running `/cleanup 1d` and seeing yourself NOT in candidates because LastReactionAt is recent.)
6. **Inline read-only** - type `@e2e_test_bot stats top` in the chat. Carousel shows three options. Tap "🏆 /stats top". Bot replies in the chat as if you'd typed the slash command.
7. **Inline destructive (cancel path)** - `@e2e_test_bot warn @<some other member> testing`. Carousel offers a preview card. Tap it -> message appears with [✅ Подтвердить] [❌ Отмена]. Tap Cancel -> message edits to "❌ Действие отменено" and buttons disappear.
8. **Inline destructive (apply path)** - repeat 7 but tap Подтвердить -> message edits to "⚠️ @<user> предупреждён (1/3)" with reason. Re-tapping the (now-empty) button must be a no-op or alert.
9. **Inline guard: actor mismatch** - repeat 7 from a second device/account (if you have one). Try to tap Подтвердить -> alert "Только инициатор команды может её подтвердить". Pending should not execute.
10. **Cleanup empty path** - `@e2e_test_bot cleanup 1d`. Tap "📋 Показать кандидатов". On a fresh chat with one active member (you), the body shows "Кандидатов на чистку нет." Pending is deleted.
11. **Cleanup populated path** - only after the bot has observed at least one inactive (or never-active) member and threshold > MinThreshold (24h). The preview lists candidates; tap "✅ Кикнуть всех" -> "🧹 Чистка запущена" -> progress edits -> final report. **DO NOT run this against a real chat with members you don't want kicked.** Test with a separate clean chat.
12. **Healthcheck** - in another terminal: `curl http://localhost:8080/health` -> `{"status":"ok"}`. After SMOKE_TIMEOUT exits, retry: connection refused (server stopped).
13. **Version** - `curl http://localhost:8080/version` -> JSON with build info.
14. **Graceful shutdown** - SIGINT (ctrl-C) before SMOKE_TIMEOUT. JSON log shows "shutdown signal received" -> "handler stopped, stats flushed" within 10s.

If any step fails, capture the JSON log around the failure and the bot's reply (or absence of reply) before continuing.

## What exists now

- Go modules across `cmd/{bidlobot,bidlobot-backup}`, `internal/{bot,domain/{stats,moderation,membership,pending,cleanup},shared,shared/{ratelimit,retry,tgclient},storage,testutil,text}`
- Subscription types: `message`, `callback_query`, `my_chat_member`, `chat_member`, `message_reaction`, `inline_query`
- bbolt with 10 buckets (`profiles`, `profiles_by_chat` kept empty for archive revival; `stats`, `stats_by_chat`, `members`, `members_by_chat`, `chats`, `warnings`, `warns_by_target`, `pending_actions`)
- AdminCache (60 s TTL, invalidated on `chat_member` updates)
- `RECORD_UPDATES=path` env activates JSONL recorder middleware
- `HEALTH_PORT` env (default 8080, 0 disables) controls /health and /version listener
- `go build` clean, `go vet` clean, `go test -race` green

## Test environment

User has a test supergroup with the bot already added as administrator with `can_restrict_members`. `TG_BOT_TOKEN` is in `.env`. Bot reads all messages (privacy disabled).

## What does NOT exist

- Mini-games (Phase 4 agent finishing in worktree-agent-a58a57ebfa140844f)
- End-to-end run against the real bot (blocked on user flipping BotFather flags above)
- Real-chat verification of cleanup workflow with kicks (run after BotFather setup is complete)

## Phase 5 deliverables (production readiness)

- **Per-chat outgoing rate limiter** in `internal/shared/ratelimit/`. 15 req/min per chat (1 token / 4s), FIFO queue capped at 50, drop oldest on overflow with WARN log. Lazy worker spawn with idle reaper so unused chats do not consume goroutines.
- **Retry with backoff** in `internal/shared/retry/`. 429 sleeps `retry_after` + 10% jitter, retry once. 5xx walks 1s/2s/4s/8s exponential ladder with jitter, up to 4 attempts. Other 4xx surface immediately. Context cancellation aborts.
- **Migration handler** for `migrate_to_chat_id`: `storage.MigrateChatID` rewrites all bbolt records keyed by abs(old) -> abs(new) across stats, members, chats, warnings buckets in a single transaction. The `tgclient.Client` wrapper detects the 400+migrate response, runs the migration, invalidates the admin cache, rewrites the request and replays.
- **Composed wrapper** in `internal/shared/tgclient/`. Outer migration -> retry -> per-chat rate limit. Read-only methods (GetMe, GetChat, GetChatMember, GetChatAdministrators) bypass the rate limiter; AnswerCallbackQuery/AnswerInlineQuery skip rate-limiting too because of their strict server-side timeout.
- **Health endpoint** at `internal/bot/health.go`. `GET /health` returns 200 ok if DB is open, last update was < 5 min ago (with startup grace), and a cached GetMe round-trip works. Otherwise 503 with reason. `GET /version` returns runtime/debug.ReadBuildInfo. Listener bound to `HEALTH_PORT` (default 8080); 0 disables.
- **Graceful shutdown**: `App.Stop()` calls `BotHandler.StopWithContext` with a 10s deadline AND waits up to the same deadline on a per-update WaitGroup tracked by an `inFlightMiddleware`. Stats flush and health server stop happen after handlers settle.
- **Backup tooling**: `cmd/bidlobot-backup` is a Go binary that opens the DB ReadOnly and uses `db.View(tx -> tx.WriteTo)` for a true online snapshot. `scripts/backup.sh` is the operator wrapper with `flock -n` for non-overlapping runs and rotation keeping the 7 newest by mtime. **Tradeoff documented in script header**: Go binary requires the bot to NOT hold the exclusive write lock, so a running bot forces fallback to plain `cp`. cp is best-effort - bbolt's double meta page recovers torn meta on next open, but for a true point-in-time backup, stop the bot first.
- **CI pipeline**: `.github/workflows/ci.yml` runs go vet, go test -race -cover, go build on every push and PR to master/main. Uses `actions/setup-go@v5` (built-in cache) and `actions/upload-artifact@v4` for coverage.
- **Config validation + version flags**: `cmd/bidlobot/config.go` validates TG_BOT_TOKEN format (`\d+:[A-Za-z0-9_-]{35,}`), DB_PATH writability, HEALTH_PORT range, LOG_LEVEL. Errors aggregated via `errors.Join`. `--version` prints build banner. `--check-config` exits 0/1 without opening the database.

## Anti-patterns (carry-over)

1. **telego methods require `context.Context` as first arg.** Every API call: `bot.SendMessage(ctx, params)`.
2. **telego `Predicate` signature:** `func(ctx context.Context, update telego.Update) bool`.
3. **telego `Use()` takes Handler, not a middleware wrapper.** Middleware = Handler that calls `ctx.Next(update)`.
4. **`MemberUser()` returns `telego.User` (value), not pointer.** Cannot compare to nil.
5. **`ChatPermissions` fields are `*bool`, not `bool`.** Must create bool vars and pass pointers.
6. **stats counting must be middleware (`Use`), not handler.** telego routes to first match only.

## Anti-patterns (new constraints from group-management scope)

7. **Bot API has no `getChatMembers`.** Member list is built bottom-up from observed events.
8. **`message_reaction` updates require bot admin + explicit `"message_reaction"` in `allowed_updates`.** Reactions from other bots are filtered.
9. **`InlineQuery` carries no `chat_id`** - only `chat_type`. Defer admin checks to callback after preview is selected.
10. **Inline result is sent as the user's message** with attached `reply_markup`. Callback handlers run with `query.From` = user, `query.Message.Chat.ID` = real chat - both required for admin verification before destructive action.
11. **Kick = `banChatMember` then `unbanChatMember(only_if_banned=true)`.** Without the unban step the user is permanently banned.
12. **Cleanup preview must declare the observation window.** Bot only knows about users it has observed; users silent since before bot installation are invisible. Include this in every preview message.
