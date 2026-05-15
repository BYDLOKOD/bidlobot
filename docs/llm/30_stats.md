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
- Users: `Name (@username)` when both are known; `@username` if no
  name. Counting is always keyed by the stable `user_id`, never by
  name, so distinct same-name members are separate rows (the legacy
  chat-export.org instead merged by name string - this is stricter).
  When there is **no @username** the display name alone is not unique
  (several members can share one; Telegram-Desktop-imported users carry
  no username at all), so the stats resolver appends the numeric id:
  `Name (id 12345)`. The id drops off automatically once the user
  writes live and a globally-unique @handle is captured
  (`SourceMessage` overwrites `SourceImport`). `shared.UserDisplayFull`
  builds the name/handle part (HTML-safe; renderers must not re-escape);
  the id suffix is added by the membership display resolver where the
  id is known.

## Monthly statistics (retroactive nominations)

A parallel engine (`internal/domain/monthstats`, bbolt buckets
`stats_month` / `stats_month_idx` / `stats_month_state` /
`stats_month_summary`) reproduces the legacy chat-export.org per-calendar-
month report. It is independent of the lifetime `stats` model above (that
stays unchanged): both the live message handler and the history importer
feed the same additive per-(chat, "YYYY-MM", user) counters through one
counting contract (`monthstats.ExtractSample`), so a chat's monthly
numbers converge regardless of how the data arrived.

**Counted dimensions per user per month:** message count, rune count
(code points of text+caption), and entity-type tallies for
`custom_emoji`, `code`, `mention`, `bot_command` (Bot API
`MessageEntity.type`, identical vocabulary in the Telegram Desktop export
`text_entities[].type`), plus a configurable keyword count (default regex
`(?i)курсор|cursor`, the legacy "курсорист" meme). Per month a singleton
tracks total messages, total runes, and the single longest message
(author + excerpt truncated to 400 runes; ranking uses the true length).

**Counting rules** match the live exclusion predicate exactly (non-bot,
not anonymous admin, no `sender_chat`, has content). Documented legacy
deviations: the "20+ messages" cohort uses the legacy code's strict `>20`;
char length is Go rune (code-point) count, not Clojure UTF-16 length
(differs only for astral characters in the longest-message ranking);
percentages use integer truncation (`part*100/total`); leaderboards
tie-break on earlier `first_seen` (deterministic, matches `/stats top`);
entity/keyword nominations drop zero-score users, message/char boards do
not (mirrors the legacy `remove zero?` placement).

**Seal lifecycle & cache.** The in-progress month is rendered fresh from
the DB+buffer merge on every call and never memoized. A past month is
immutable (the 60s buffer has flushed; <=60s tail loss is the same
tolerance the lifetime buffer documents) and its rendered HTML is
memoized in `stats_month_summary`. A memoized summary is auto-invalidated
when a later import advances `MonthState.UpdatedAt` past the summary's
`BuiltAt`, or when `SummarySchemaVer` changes - no explicit cache-bust on
re-import.

**Idempotency.** `MonthState` holds a per-chat imported-message-id
high-water-mark, a sealed-month set, and `LiveTrackStart` (the first live
message ts for the chat, persisted by the buffer's first flush). The
importer skips export rows with `id <= ImportHWM` and rows with
`ts >= LiveTrackStart` **only when `LiveTrackStart` is non-zero** (a chat
with no live data yet - e.g. the bot not added - imports everything), so
every message is counted exactly once across the live and import paths.

**Commands.** `/stats months` lists months with data (newest first);
`/stats month [YYYY-MM]` renders one month's board (default: the newest
complete month). Available on the public read-only `/stats` surface and
the DM console, mirroring the existing `/stats` subcommands.
