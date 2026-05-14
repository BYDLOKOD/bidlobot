---
id: moderation
kind: spec
---

# Moderation

See also: [10_scope.md](10_scope.md), [50_telegram.md](50_telegram.md).

## Admin model

Source of truth: `getChatAdministrators` (Telegram API). No bot-managed admin list.

Permission check flow:
1. Call `getChatAdministrators` (or cache)
2. Filter: exclude `user.is_bot == true`
3. Check `from.id` in filtered list
4. Not found -> "You don't have permission to use this command."

**Cache:** 5 minutes per-chat. Invalidated on `chat_member_updated` event.

**Bot self-check:** `getChatMember(chat_id, bot_id)` cached same TTL. `can_restrict_members == false` -> "Bot needs 'Restrict Members' permission to perform this action."

## Target resolution

All moderation commands support two modes:
1. **Reply** - reply to offender's message. Target = `reply_to_message.from`
2. **Explicit** - `/warn @username reason`. Target by username.

Reply takes priority. If command is a reply AND contains @username, reply target is used, rest is treated as reason.

## Target validation (applies to warn, mute, ban)

- Bot -> "Can't {action} a bot."
- Admin -> "Can't {action} an administrator."
- Self -> "Can't {action} yourself."

## Warnings

**`/warn @username [reason]`** or reply

1. Check caller is admin and not anonymous
2. Resolve target
3. Validate target
4. Create record: `warn:{uuid}` -> `{target_user_id, chat_id, issuer_user_id, reason, timestamp, active: true}`
5. Count active warnings atomically (compare-and-swap or DB serialization)
6. Response:

Warnings 1-3:
```
⚠️ @username warned (2/3)
Reason: Spam links
Issued by: @admin
```

Warnings 4+:
```
⚠️ @username warned (4 total). Auto-mute threshold already reached.
```

No reason -> omit Reason line.

**Auto-escalation at 3:** mute 24h. Message:
```
🔇 @username muted for 24h (3 warnings reached)
```

If mute fails (target became admin, bot lost rights) -> "⚠️ Warning recorded. Auto-mute failed: {API error reason}."

After 3: no further auto-mutes. Counter doesn't reset. Admin must `/warns clear` to restart escalation.

**`/warns @username`** - view active warnings (available to all members)
```
Warnings for @username (2/3)
1. Spam links - by @admin1, Mar 4, 2026
2. Off-topic flood - by @admin2, Mar 5, 2026
```

**`/warns clear @username`** - admin only. Marks all records `active: false` (audit trail preserved). Response: "Warnings cleared for @username."

## Mute

**`/mute @username [duration]`** or reply

Durations: `30m`, `1h`, `2h`, `12h`, `1d`, `7d`, `30d`. Default: `1h`. Min: 1 min, max: 366 days.

Invalid format -> "Invalid duration format. Examples: 30m, 1h, 7d."

API call: `restrictChatMember` with all permissions `false`, `until_date` = now + duration.

API errors:
- `"user is an administrator"` -> "Can't mute an administrator."
- `"not enough rights"` -> "Bot needs 'Restrict Members' permission."
- Other -> log, respond "Failed to mute. Please try again."

```
🔇 @username muted for 1h
By: @admin
```

**`/unmute @username`** or reply

Restore chat defaults: call `getChat(chat_id)` -> use `permissions` field. If `permissions` is null -> fallback all-true.

API call: `restrictChatMember` with chat's default permissions.

```
🔊 @username unmuted
By: @admin
```

## Ban

**`/ban @username [reason]`** or reply

API call: `banChatMember(chat_id, user_id, revoke_messages: false)`. Permanent (no until_date).

No reason -> omit Reason line.

```
🚫 @username banned
Reason: Repeated violations
By: @admin
```

**`/unban @username`**

1. Check admin
2. Resolve target
3. Pre-check: `getChatMember(chat_id, user_id)` - if `status != "kicked"` -> "User is not banned."
4. Call `unbanChatMember(chat_id, user_id, only_if_banned: true)`

**`only_if_banned: true` mandatory** - without it the method removes active members.

```
✅ @username unbanned
```

## Data on ban

Profile, warnings, stats - all preserved. Available via `/profile`, `/warns`. Restored on `/unban` + rejoin.
