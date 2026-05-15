---
id: history-import
kind: spec
touches: cmd/bidlobot-import/, internal/domain/membership/, internal/storage/membership_repo.go
---

# History import

Source of truth for the `bidlobot-import` bootstrap path.

## Problem

Bot API has no `getChatMembers` and no message history. On a fresh
deploy the membership table is empty, so `cleanup 6mo` on day one finds
nobody. The bot can only ever act on users it observed live.

## Solution

A one-time import of a Telegram Desktop **"Export chat history"** JSON
(the per-chat menu: `⋯ -> Export chat history -> Format: JSON`, NOT
Settings -> Export Telegram Data, which for a public group contains only
the operator's own messages).

`cmd/bidlobot-import` streams that JSON and seeds the same `members`
bucket the live tracker writes, so `cleanup` and `/stats` both work on
the historical data with zero changes to their own code.

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

## Rules

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
5. `SetMessageCount` is applied as `max(existing, value)` - re-running
   the same import is idempotent and never reduces a realtime count
   accumulated since deploy.
6. `Now = maxTS` so `LastSeenAt` reflects genuine last activity and the
   cleanup preview sorts by real staleness, not import time.
7. The chat record's `InstalledAt` is set to the earliest observed
   event so `cleanup`'s ObservationWindow honestly reports how far back
   the data goes.
8. Live events (`SourceMessage` etc.) overwrite `SourceImport`:
   "observed for real" always beats "imported".

## Ghost members (known gap)

A user who is in the chat now but never wrote AND has no join event in
the exported range is invisible to this path. Enumerating them needs
the MTProto User API (`channels.getParticipants`), which is out of
scope here (phone+2FA, account-ban risk). Documented, not silently
ignored.

## Operational constraint

bbolt holds an exclusive flock while the bot runs. A real import
requires the bot stopped:

```sh
# Safe preview against the LIVE bot (never opens the DB):
docker compose run --rm -v /path/result.json:/tmp/r.json bot \
  bidlobot-import --json /tmp/r.json --chat-id -1009000002 --dry-run

# Real import:
docker compose stop bot
docker compose run --rm -v /path/result.json:/tmp/r.json bot \
  bidlobot-import --json /tmp/r.json --chat-id -1009000002
docker compose start bot
```

`--chat-id` is mandatory (signed form) as a guard against importing an
export into the wrong chat. `--dry-run` parses and reports without
opening the DB, so it is safe while the bot is live.
