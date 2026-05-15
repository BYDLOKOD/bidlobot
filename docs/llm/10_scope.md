---
id: scope
kind: spec
---

# Scope & Decisions

## What this bot does

Group management for IT supergroups in Telegram. Four capability areas:

1. **Statistics** - message counters per user, top contributors, activity reports.
2. **Moderation** - warn/mute/ban with 3-strike auto-mute. Telegram-native admin model.
3. **Inactive cleanup** - admin can kick users who never wrote messages and never reacted within a configurable window. Read-only members (those who only react) are preserved.
4. **Mini-games** - small chat-engagement games (dice, reaction-battle, code-quiz) callable inline or via slash commands.

**Command surfaces (revised 2026-05-15 after the privacy rework):**

- **Moderation + cleanup: DM console only.** The admin opens a private
  chat with the bot, `/start`, picks the target chat once, then issues
  `/warn /warns /mute /unmute /ban /unban /cleanup` privately. Ban and
  cleanup require an in-DM confirm. Nothing is visible to chat members.
- **Public group: read-only + games only.** `/stats` and `/dice
  /battle /quiz` (per-user cooldown-gated). A moderation verb typed in
  the group is deleted and the admin is redirected to DM - it never
  executes publicly.
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
| YouTube Summary | Unrelated to core, obscure LLM provider dependency (GLM/BigModel.cn) |
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
