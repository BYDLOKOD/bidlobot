---
id: telegram
kind: guide
touches:
  - internal/shared/telegram.go
  - internal/shared/tgclient/
  - internal/bot/cooldown.go
  - internal/bot/setup.go
  - internal/bot/app.go
  - internal/bot/middleware.go
  - internal/bot/membership.go
  - internal/bot/captcha.go
  - internal/bot/routes.go
  - internal/storage/migrate.go
written: 2026-05-14
updated: 2026-09-17
---

# Telegram API Reference

Behavior specifics relevant to BidloBot. Not a full API reference - only project-relevant details.

See also: [PRD.md](PRD.md), [30_stats.md](30_stats.md), [60_architecture.md](60_architecture.md).

## Chat types

| Type | Bot behavior |
|------|-------------|
| `private` | `/help` `/start` (same help text), `/chats` (owner console) |
| `group` | Rejected: "Add the bot to a supergroup." |
| `supergroup` | Full functionality |
| `channel` | Ignored |

Groups auto-migrate to supergroup at ~200 members, public username, or persistent history. Migration changes chat_id - handle `migrate_to_chat_id` error.

## Anonymous admins

Messages from anonymous admins arrive with `from.id == 1087968824` (GroupAnonymousBot).

Detection: `from.id == 1087968824` or `from.is_bot == true && from.username == "GroupAnonymousBot"`.

- Stats: not counted; content middlewares skip; summarize recorder skips.
- `/summarize`: rejected - anonymous admins cannot invoke (no identifiable `From.ID`).
- Captcha: never fires for anonymous-admin joins.

## Linked channel messages

Auto-forwarded from linked channel. `sender_chat` field present instead of `from`. Bot ignores entirely (not counted, not sanitized, not reposted).

## Deep linking

Not used anymore (the profile FSM with `reg_`/`upd_` payloads was archived with the bio domain).

## Bot command scopes

Set via `setMyCommands` at startup (`setup.go`):

| Scope | Commands |
|-------|---------|
| `BotCommandScopeAllGroupChats` | stats, summarize, dice, battle, quiz, poll, 8ball, roast, praise, rep, reptop, guess, hangman, duel, trivia, refs, refreg, flush |
| `BotCommandScopeAllChatAdministrators` | group commands + refreport |
| `BotCommandScopeChat(owner)` | start, help, chats |

Telegram resolves most-specific scope per user. Cyrillic aliases
(`/итог`, `/ref`, `/ref-reg`) are typed-only: `setMyCommands` rejects
non-ASCII names. Command menu visibility only - the bot validates
permissions server-side (AdminCache for `/summarize`/`/refreport`).

## Bot onboarding

On `my_chat_member` update (bot added to chat):

1. Actor != `BOT_OWNER_ID` -> immediate `LeaveChat` + owner DM notice
   "Unauthorized bot admission rejected" (suppressed after 2 lifetime
   attempts per actor). Owner-gated installation.
2. Owner, admin status (`administrator`) -> "BidloBot подключён..." message, start working.
3. Owner, member (not admin) -> "I need administrator rights to function. Please promote me with 'Restrict Members' permission."
4. Regular group (not supergroup) -> "I only work in supergroups. Please upgrade this group."

## New-member captcha

On `chat_member` (`left|kicked -> member`) with `CAPTCHA_ENABLED`:
mute newcomer, post `a + b = ?` inline challenge (4 buttons). Wrong
answer -> ban+unban kick (rejoinable). Correct -> unmute + async
welcome animation with onboarding questions. Unanswered ->
`CAPTCHA_TIMEOUT` sweep kicks. Details: [65_admission.md](65_admission.md).

## Edited messages

Bot does not process `edited_message` updates (not subscribed). Standard behavior.

## Mentions without command

`@botname` without command -> no reaction.

## Callback queries

Inline keyboards use `callback_data` (max 64 bytes). Bot must always call `answerCallbackQuery` to dismiss spinner, even on error.

Callback ordering matters (first-match-wins): captcha `cap:` -> referral `rf:` -> owner chats `oc:` -> `v1:` catch-all dispatcher.

Callback queries work from group messages - `callback_query.message.chat.id` contains the group ID. Timeout ~10-15s.

## Rate limits

Outgoing: bot limits itself to 15 messages/min per chat (below Telegram's 20/min). Excess queued (not dropped). Queue: per-chat, max 50. Overflow: oldest dropped with logging.

Telegram 429 error: respect `retry_after` field (seconds) + 10% jitter. `retry_after` is per-chat since Feb 2025.

Retry classes in `shared/retry` (all with 10% jitter, caller cancellation never retried): 429 -> one retry after `retry_after`; 5xx (Telegram API error or bare HTTP status) -> 1/2/4/8s ladder, 4 attempts; transport (DNS, connect, TLS, read/write timeout, connection reset) -> 1/2/4s ladder, 3 attempts. Every attempt runs under its own deadline: 20s for control calls, 240s for media sends (`tgclient`). The outbound HTTP client bounds a call even when the caller passes a deadline-less context: 65s read (above the 30s long poll), 240s write.

A media retry re-sends the same body: `tgclient` rewinds every file-backed upload body (media, thumbnail, cover, album item) to its start before each attempt, because telego streams a body into the multipart part once and a reader left at EOF uploads an empty part, which Telegram rejects with `400 file must be non-empty` - a class the ladder never retries (fixed 2026-09-17; see `devlog/11_upload_retry_rewind.md`).

Per-user command cooldown (`internal/bot/cooldown.go`, applied by `gateMsg` to games, `/stats`, `/summarize`, `/refs*`, `/flush`): a user may trigger a given command once per its window (5-30s). An over-frequency call is dropped (handler not run) but is **not** fully silent: exactly **one** "slow down" notice is sent per window per (user,command) - bounded so a flooder cannot amplify, while a normal user still gets feedback. A fresh allowed call resets the notice state. The notice goes through the rate-limited sender; absent sender (minimal/test app) -> no notice, drop stays silent.

## Message formatting

HTML parse mode. Escape `<`, `>`, `&` in user-provided text. Max message length: 4096 chars. Summary output is plain text (no ParseMode) + `defuseMentions` (U+2060 after `@`).

## Error handling

API errors not exposed to users. Bot logs original error, responds with human-readable message.

A panic in any route is recovered by `App.recoverMiddleware`, the outermost
handler in the update chain (`app.go`): the panic and its stack are logged as
`update handler panic recovered` and the process keeps serving. Background
work spawned by handlers runs through `shared.Go`, which recovers the same way
(`background goroutine panic recovered`).

| Telegram error | Bot response |
|---------------|-------------|
| `"not enough rights"` | "Bot needs 'Restrict Members' permission." + invalidate cache |
| `"user is an administrator"` | "Can't {action} an administrator." |
| `"bot was blocked by the user"` | Log, no response |
| `"chat not found"` | Log, no response |
| `"query is too old"` | Log, no response |

On `"not enough rights"` mid-operation: invalidate admin cache, re-check via `getChatMember`, respond "Bot lost administrator rights."

## Group migration

On `migrate_to_chat_id` in API response:
1. Update DB records from old abs(chat_id) to new abs(chat_id) - stats, members, chats, warnings, monthstats, dailystats, referrals are rekeyed. Known gap: reputation/captcha/deferred/admission/gracekick/game leaderboards are NOT rekeyed ([60_architecture.md](60_architecture.md)).
2. Retry original API call with new chat_id
3. Log migration
4. Invalidate admin cache for old chat_id

## Graceful shutdown

Signal: SIGTERM or SIGINT.

1. Stop polling (no new getUpdates calls)
2. Wait for in-flight handlers (timeout: 10 seconds)
3. Flush stats buffer to DB
4. Close DB connection
5. Exit

Heavy media goroutines (TikTok/xpost/captcha welcome) are NOT tracked -
best-effort, may be lost on shutdown. They are launched through
`shared.Go`, so a panic inside one is logged instead of killing the process.

## Logging

Structured JSON. Fields: `chat_id`, `user_id`, `command`, `duration_ms`, `error`.

Levels: ERROR (API/DB failures), WARN (rate limits, permission issues, slow handlers >100ms), INFO (commands), DEBUG (messages).

Never log: message text, profile content, bot token, `DEEPSEEK_API_KEY` (the omp CLI reads it from env; the Go binary never sees it).
