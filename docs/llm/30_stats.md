---
id: stats
kind: spec
---

# Chat Statistics

See also: [10_scope.md](10_scope.md), [50_telegram.md](50_telegram.md).

## Requirement

Bot must be admin in the chat (admin sees all messages regardless of privacy mode). Stats work without admin status but only count messages the bot can see.

## Counting rules

Every incoming message in a supergroup with a real `from.id` increments the sender's counter.

**Counted:** any content type (text, photo, video, sticker, voice, poll, location, etc.) if `from.is_bot == false`. Forwarded messages attributed to the forwarder.

**Not counted:**
- Bot messages (`from.is_bot == true`)
- Anonymous admin messages (`from.id == 1087968824`)
- Linked channel messages (`sender_chat` present)
- Service messages (no content fields)
- Edited messages (`edited_message` updates)

## Data model

Key: `stats:{user_id}:{abs(chat_id)}`

Fields: `message_count`, `first_seen` (timestamp), `last_seen` (timestamp).

## Buffering

In-memory map of counters. Flush to DB every 60 seconds. Final flush on graceful shutdown.

**Queries read DB + buffer.** Result = persisted + buffered. Never show stale data when fresh data is in memory.

Crash = loss of up to 60 seconds. Acceptable for stats.

## Commands

**`/stats`** - chat overview
```
Chat Statistics
Total messages: 12,847
Total users: 156
Average per user: 82
Most active: @poweruser (2,341 messages)
Tracking since: Mar 4, 2026
```

**`/stats top`** - top 5. Tie-break: earlier `first_seen` ranks higher.
```
Top Contributors
1. @poweruser - 2,341
2. @activeguy - 1,892
3. @coder42 - 1,456
4. @debater - 1,203
5. @helper - 987
```

**`/stats today`** - UTC 00:00 boundary
```
Today's Activity
Messages: 127
Active users: 23
```

**`/stats @username`** or **`/stats {user_id}`** - per-user
```
Stats for @username
Messages: 1,892
Rank: #2 of 156
First seen: Jan 15, 2026
Last seen: Today
```

**Errors:**
- User not found -> "User not found in chat statistics."
- Unknown subcommand -> "Unknown subcommand. Available: top, today, @username."
- In private chat -> "Statistics are only available in group chats."

## Formatting

- Numbers: thousands separator comma (`12,847`)
- Dates: `Mon DD, YYYY`. Today (UTC) -> `Today`
- Users: `@username`. No username -> first_name
