---
id: scope
kind: spec
---

# Scope & Decisions

## What this bot does

Group management for IT supergroups in Telegram. Six capability areas:

1. **Statistics** - message counters per user, top contributors, activity reports.
2. **Moderation** - warn/mute/ban with 3-strike auto-mute. Telegram-native admin model.
3. **Inactive cleanup** - removes members the bot has _observed_ go silent (no message and no reaction within a configurable window). Two surfaces share one evidence-graded engine (`internal/domain/cleanup`):
   - **Manual `/cleanup <period>`** (DM, admin-confirmed): preview splits _proven-stale_ (the bot saw them, the activity is old) from _no recorded activity at all_ (a data gap - import-only / react-only members). Only proven-stale is auto-kicked; the data-gap group is shown named for manual review and never kicked blind. Names/@handles are resolved live via `getChatMember`, and a loud warning fires when the requested period exceeds the window the bot actually has data for.
   - **Daily lifecycle** (`internal/domain/gracekick`, opt-in, OFF by default): once a day, tag a batch of _proven-stale only_ members publicly, give a grace window (default 3 days), then kick those who did not write or react in time. Members who reappear are spared. The no-evidence group is never touched by this automatic path.
4. **Mini-games** - chat-engagement games (dice, reaction-battle,
   code-quiz, native poll, 8ball, roast/praise, guess, hangman, duel,
   IT-trivia) callable inline or via slash commands. Spec:
   [25_games.md](25_games.md).
5. **Retroactive monthly stats** - per-calendar-month "nominations"
   (chat-export.org parity), live- and import-fed. [30_stats.md](30_stats.md).
6. **YouTube `si=` sanitizer** - strips the share-tracking param
   (delete + attributed repost). This is NOT the dropped "YouTube
   Summary" (no LLM). [55_youtube_sanitizer.md](55_youtube_sanitizer.md).

**Command surfaces (revised 2026-05-15 after the privacy rework):**

- **Moderation + manual cleanup: DM console only.** The admin opens a
  private chat with the bot, `/start`, picks the target chat once, then
  issues `/warn /warns /mute /unmute /ban /unban /cleanup` privately.
  Ban and cleanup require an in-DM confirm. Nothing is visible to chat
  members.
- **Exception - the daily inactive lifecycle is PUBLIC by design**
  (owner decision, 2026-05-15). It deliberately overrides the
  "nothing visible to chat members" invariant: the social pressure of a
  public @-tag is the mechanism. Scoped tightly: opt-in (OFF unless
  `CLEANUP_DAILY_ENABLED`), proven-stale members only (never the
  no-evidence data gap), batch-capped per day, with a grace window and a
  "write or react to stay" rule. This is the only feature that posts in
  the group on the bot's own initiative.
- **Public group: read-only + games only.** `/stats` (incl. `month` /
  `months`) and the mini-games (`/dice /battle /quiz /poll /8ball
  /roast /praise /guess /hangman /duel /trivia`), per-user
  cooldown-gated with a bounded over-frequency notice. A moderation
  verb typed in the group is deleted and the admin is redirected to DM
  - it never executes publicly. `/import` is DM-only.
- **Inline is NOT a private surface.** A chosen inline result is posted
  as a public message into the originating chat, so inline can never
  hide a moderation action. The earlier "inline = primary, private
  surface" premise was wrong; inline moderation verbs now return a
  generic "use DM" hint and create no pending. See
  [70_deployment.md](70_deployment.md) and the telegram-api-constraints
  memory.

## Scope history

**Pivot 2026-05-14.** Original Go rewrite (commit `9851c0b`, tag `v0-bio-archive`) included a full bio/profile domain with FSM-based registration. The bio feature is archived to branch `archive/profiles-bio` and may return later. Current focus is group management for the user's own 200+ member chat.

## Archived (not in master, but preserved)

| Feature | Where to find it |
|---------|------------------|
| Bio / profile registration FSM | `archive/profiles-bio` branch, `v0-bio-archive` tag |

## Permanently dropped (must not return without explicit ask)

| Feature | Why |
|---------|-----|
| YouTube Summary | Unrelated to core, obscure LLM provider dependency (GLM/BigModel.cn). NOTE: distinct from the shipped YouTube `si=` *sanitizer* ([55_youtube_sanitizer.md](55_youtube_sanitizer.md)) - that one has no LLM and is in scope. A GLM chat-summarization workstream exists in a parallel session but is NOT in master/prod. |
| Inline Query DSL parser | Replaced by inline-mode autocomplete with structured commands |
| Salary field | Public salary in group chat = guaranteed conflict |
| zen-lang config | Env vars suffice |
| i18n switching | All user-facing strings in Russian |
| Bot-managed admin list | Duplicates Telegram's native admin system |

## Deployment model

Single Go binary. Long-polling. Embedded bbolt key-value database.

Env vars:
- `TG_BOT_TOKEN` (required)
- `DB_PATH` (default: `./data`)
- `LOG_LEVEL` (default: `info`)
- `RECORD_UPDATES` (optional path to JSONL recorder)
- `CLEANUP_DAILY_ENABLED` (default: `false`) - opt-in daily public tag->grace->kick
- `CLEANUP_DAILY_AT` (default: `10:00`, UTC `HH:MM`)
- `CLEANUP_DAILY_THRESHOLD` (default: `6mo`; `30d`/`6mo`/`1y`/Go duration)
- `CLEANUP_GRACE` (default: `72h`)
- `CLEANUP_DAILY_BATCH` (default: `15`)

## ID scheme

Format: `{entity}:{user_id}:{abs(chat_id)}`

Chat IDs stored as absolute values (supergroup IDs are negative in Telegram). Users identified by `user_id` (stable, never changes). Username is display-only and tracked for last-known mapping.

Examples:
- Stats: `s:00000000000000000123:00001001234567890`
- Membership: `m:00000000000000000123:00001001234567890`  *(Phase 1)*
- Warning: `w:{uuid}` (globally unique, chat_id and target_user_id inside the document)

## Commands (current state)

| Command | Context | Access |
|---------|---------|--------|
| `/stats [top\|today\|@user]` | supergroup | all |
| `/warn @user [reason]` | supergroup | admins |
| `/warns @user` | supergroup | all |
| `/warns clear @user` | supergroup | admins |
| `/mute @user [duration]` | supergroup | admins |
| `/unmute @user` | supergroup | admins |
| `/ban @user [reason]` | supergroup | admins |
| `/unban @user` | supergroup | admins |
| `/help` | supergroup + DM | all |

Inline mode (`@bidlobot ...`) ships in Phase 2 and exposes the same commands plus `cleanup`, `dice`, `battle`, `quiz`.

## Performance targets

- Update processing: < 100 ms (p95)
- Memory: < 100 MB at 50 active chats
- Outgoing rate: <= 15 msg/min/chat (below Telegram's 20/min, see [50_telegram.md](50_telegram.md))
