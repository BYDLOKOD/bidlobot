---
id: history-import
kind: spec
touches: internal/histimport/, internal/domain/membership/, internal/domain/monthstats/, internal/bot/dm_console.go
---

# History import

Source of truth for the DM `/import` bootstrap path.

See also: [30_stats.md](30_stats.md), [40_moderation.md](40_moderation.md).

## Problem

Bot API has no `getChatMembers` and no message history. On a fresh
deploy both the membership table and the monthly statistics are empty,
so `/cleanup 6mo` on day one finds nobody and `/stats month` shows
nothing for any pre-bot month. The bot can only ever act on users it
observed live.

## Solution

A one-time (or repeated, date-sliced) import of a Telegram Desktop
**"Export chat history"** JSON, delivered to the bot **in a private
chat** - no server access and no bot restart.

Admin flow:

1. Add the bot to the chat as **admin** with the right to restrict
   members. The DM console only manages chats where the bot is admin
   and the caller is admin, so the required order is **add bot ->
   `/import`** (importing into a chat the bot does not administer is
   rejected).
2. Telegram Desktop -> open the chat -> `⋯ -> Export chat history ->
   Format: JSON` (the per-chat menu, NOT Settings -> Export Telegram
   Data, which for a public group contains only the operator's own
   messages).
3. Open a private chat with the bot, send `/import`, then send the
   export file.

The in-process importer (`internal/histimport`) streams that JSON and
feeds the same counting contracts the live handlers use, so `/cleanup`
and `/stats month` both work on the historical data with zero changes
to their own code.

## File size & compression

The Bot API caps a bot file download at **20 MB**. A real export is
~31 MB raw JSON, ~4 MB gzipped, so the user sends the JSON
**compressed** as `.gz` or `.zip`. The bot auto-detects the container
and decompresses **in-process**; an uncompressed file under 20 MB is
also accepted as-is. Files Telegram refuses to hand over (over 20 MB)
must be re-sent compressed.

## Export schema (verified against a real 41k-message export)

```json
{ "name": "...", "type": "public_supergroup", "id": 123,
  "messages": [
    {"id":1,"type":"message","date":"2025-08-05T00:02:00",
     "date_unixtime":"1754341320","from":"Олег",
     "from_id":"user1786612758","text":"...","text_entities":[...]},
    {"id":2,"type":"service","action":"invite_members","actor_id":"user1"}
  ]}
```

- `from_id`: `user<id>` (real member) | `channel<id>` | `chat<id>`
  (anonymous admin / linked-channel autopost - excluded).
- `from`: display name, may be `null` (service / anonymous). It is NOT
  the @username; the export has no username. Irrelevant: kicks are by
  user id.
- `date_unixtime`: reliable UTC. `date`: exporting client's local wall
  clock - fallback only.
- Reactions are NOT in the export. Member roster is NOT in the export
  (only users who wrote or appear in a service event).

## What it seeds

A single import seeds **both** sinks from one pass:

- the **membership table** (the `members` bucket the live tracker
  writes) so `/cleanup` works on pre-bot history;
- the **monthly statistics** (`internal/domain/monthstats`) so
  `/stats month` reproduces the legacy per-calendar-month report for
  pre-bot months.

### Membership rules

1. Only `type:"message"` with `from_id` matching `user<positive int>`
   becomes a member. Everything else is tallied as skipped.
2. `type:"service"` with `action` in {invite_members,
   join_group_by_link} and an `actor_id` of `user<id>` contributes a
   `JoinedAt` lower bound - catches members who joined but never wrote
   *if* the join event is inside the exported range.
3. Per user the importer aggregates: message count, earliest and latest
   message timestamp, last non-empty display name.
4. Persisted via `MemberPatch{ KnownVia: SourceImport, Status: Member,
   LastMessageAt: maxTS, SetMessageCount: count, JoinedAt:
   joinedAt|minTS, Now: maxTS }`.
5. `SetMessageCount` is applied as `max(existing, value)` - never
   reduces a realtime count accumulated since deploy.
6. `Now = maxTS` so `LastSeenAt` reflects genuine last activity and the
   cleanup preview sorts by real staleness, not import time.
7. The chat record's `InstalledAt` is set to the earliest observed
   event so `cleanup`'s ObservationWindow honestly reports how far back
   the data goes.
8. Live events (`SourceMessage` etc.) overwrite `SourceImport`:
   "observed for real" always beats "imported".

### Monthly-stats rules

Export rows pass through the same counting contract
(`monthstats.ExtractSample`) the live message handler uses, so a
chat's monthly numbers converge regardless of how the data arrived.
Counting rules and the legacy deviations are documented in
[30_stats.md](30_stats.md) ("Monthly statistics").

## Idempotency

Import is idempotent, so re-sending the same or an overlapping export
never double-counts:

- a per-chat **message-id high-water-mark** plus an **atomic state
  write** (membership `SetMessageCount` is `max`-semantics;
  `monthstats` skips rows with `id <= ImportHWM`);
- date-sliced **multiple sends** are supported and accumulate
  idempotently - export month-by-month and send each slice; overlap
  is absorbed.

The `monthstats` importer also skips rows with `ts >= LiveTrackStart`
(when non-zero), so the live and import paths count every message
exactly once. Full state-machine detail in
[30_stats.md](30_stats.md) ("Idempotency").

## No server access, no restart

In-process import shares the bot's already-open bbolt handle, so there
is **no flock conflict** - that flock conflict was the only reason the
earlier standalone CLI required stopping the bot. There is no separate
process, no server/CLI access, and no `docker compose stop` / restart
in the import procedure.

## Ghost members (known gap)

A user who is in the chat now but never wrote AND has no join event in
the exported range is invisible to this path. Enumerating them needs
the MTProto User API (`channels.getParticipants`), which is out of
scope here (phone+2FA, account-ban risk). Documented, not silently
ignored.

## Operating model: privacy mode does NOT need disabling

The Bot API offers no "metadata-only" mode: BotFather privacy is either
ON (bot sees commands / @-mentions / replies only) or OFF (bot sees the
full text of every message). `cleanup` only needs a per-user *activity
timestamp*, never content - but the platform forces all-or-nothing.

Two valid models; pick by cadence, not by default:

- **Periodic, import-driven (recommended; matches the historical
  ~3×/year `chat-rewind` cadence).** Keep privacy **ON**. Discipline:
  fresh Desktop export -> DM `/import` -> run `/cleanup`
  *immediately*. A fresh export carries every writer's last message,
  so `LastMessageAt` is current at run time -> no false positives for
  writers. The bot must be **admin** so live `message_reaction`
  updates keep `LastReactionAt` fresh for react-only members (privacy
  gates messages, not reactions). No message content ever reaches the
  bot.
- **Continuous live stats.** Disable `/setprivacy` + remove/re-add the
  bot. Live `LastMessageAt` for everyone, no import needed. Cost: full
  message content transits the bot process (it persists only
  id+timestamp+counter and never logs text - audited invariant) plus a
  per-message bbolt write.

Bot cannot self-export: no Bot API method exists for chat export,
message history, or member enumeration. The manual Desktop step is a
platform boundary, not a TODO; automating it requires an MTProto user
session (out of scope, see Ghost members).

### False positives under the periodic model (why preview+confirm is load-bearing)

`cleanup` is deliberately human-in-the-loop. Even with a fresh import,
two classes can wrongly surface as candidates - the admin must catch
them by reading the preview list + ObservationWindow before
confirming:

- a react-only member the bot never observed reacting *before* the
  import (export has no reactions, and `LastReactionAt` is only set
  from live events while the bot was admin);
- a brand-new member who joined but has not written yet
  (`LastMessageAt` zero -> looks inactive).
