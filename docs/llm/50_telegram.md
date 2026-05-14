---
id: telegram
kind: guide
---

# Telegram API Reference

Behavior specifics relevant to BidloBot. Not a full API reference - only project-relevant details.

See also: [20_profiles.md](20_profiles.md), [30_stats.md](30_stats.md), [40_moderation.md](40_moderation.md).

## Chat types

| Type | Bot behavior |
|------|-------------|
| `private` | Registration FSM, `/help`, `/cancel` |
| `group` | Rejected: "Add the bot to a supergroup." |
| `supergroup` | Full functionality |
| `channel` | Ignored |

Groups auto-migrate to supergroup at ~200 members, public username, or persistent history. Migration changes chat_id - handle `migrate_to_chat_id` error.

## Anonymous admins

Messages from anonymous admins arrive with `from.id == 1087968824` (GroupAnonymousBot).

Detection: `from.id == 1087968824` or `from.is_bot == true && from.username == "GroupAnonymousBot"`.

- Stats: not counted
- Moderation commands: rejected ("Moderation commands are not available in anonymous admin mode.")
- Profile commands: rejected ("This command requires a non-anonymous account.")

## Linked channel messages

Auto-forwarded from linked channel. `sender_chat` field present instead of `from`. Bot ignores entirely.

## Deep linking

Format: `t.me/{bot}?start={payload}`

Payload constraints: max 64 chars, `A-Za-z0-9_-`. Bot encodes `reg_{abs_chat_id}` or `upd_{abs_chat_id}`.

Flow: user clicks -> Telegram opens DM -> user presses START (if first time) -> bot receives `/start {payload}`.

If user hasn't started bot before: Telegram shows bot description + START button. Payload preserved until START pressed. Bot cannot message first - 403 "bot can't initiate conversation".

## Bot command scopes

Set via `setMyCommands` at startup:

| Scope | Commands |
|-------|---------|
| `BotCommandScopeAllPrivateChats` | /help, /cancel |
| `BotCommandScopeAllGroupChats` | /register, /profile, /update, /stats, /help |
| `BotCommandScopeAllChatAdministrators` | all commands including /warn, /warns, /mute, /unmute, /ban, /unban |

Telegram resolves most-specific scope per user. Command menu visibility only - bot must validate permissions server-side.

## Bot onboarding

On `my_chat_member` update (bot added to chat):
1. Admin status (`administrator`) -> silently start working
2. Not admin -> "I need administrator rights to function. Please promote me with 'Restrict Members' permission."
3. Regular group (not supergroup) -> "I only work in supergroups. Please upgrade this group."

## Edited messages

Bot does not process `edited_message` updates. Standard behavior.

## Mentions without command

`@botname` without command -> no reaction.

## Callback queries

Inline keyboards use `callback_data` (max 64 bytes). Bot must always call `answerCallbackQuery` to dismiss spinner, even on error.

Callback queries work from group messages - `callback_query.message.chat.id` contains the group ID.

Timeout: ~10-15 seconds. After that, query ID becomes invalid.

## Rate limits

Outgoing: bot limits itself to 15 messages/min per chat (below Telegram's 20/min). Excess queued (not dropped). Queue: per-chat, max 50. Overflow: oldest dropped with logging.

Telegram 429 error: respect `retry_after` field (seconds) + 10% jitter. `retry_after` is per-chat since Feb 2025.

## Message formatting

HTML parse mode. Escape `<`, `>`, `&` in user-provided text. Max message length: 4096 chars.

## Error handling

API errors not exposed to users. Bot logs original error, responds with human-readable message.

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
1. Update all DB records from old abs(chat_id) to new abs(chat_id)
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

## Logging

Structured JSON. Fields: `chat_id`, `user_id`, `command`, `duration_ms`, `error`.

Levels: ERROR (API/DB failures), WARN (rate limits, permission issues), INFO (commands), DEBUG (messages).

Never log: message text, profile content, bot token.
