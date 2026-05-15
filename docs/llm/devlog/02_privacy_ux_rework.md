---
id: devlog-02
kind: log
---

# Devlog #02: privacy-driven UX rework + history import

Date: 2026-05-15
Commits: 0f2f830 (feat/ux), aab4276 (docs finalization)
Duration: ~1 session

## Context

Audit of the live bot exposed a UX hole the operator stated directly:
an admin must not manage the chat in the public timeline across several
messages - moderation belongs in DM or inline. Investigation found a
deeper architectural fact: Telegram inline `InputMessageContent` always
posts publicly into the originating chat, so inline is NOT a private
surface. The only private bot↔admin surface is a DM.

Separately, `/cleanup` was useless on a real chat: Bot API has no
`getChatMembers` and no history, so on a fresh deploy the bot knows
nobody. The operator's real workflow is a Telegram Desktop chat export.

## Timeline

- Reworked moderation to a **DM console** (`internal/bot/dm_console*.go`):
  admin DMs the bot, `/start` -> pick managed chat -> warn/warns/mute/
  unmute/ban/unban/cleanup/stats. Ban + cleanup confirm in-DM,
  actor-locked, admin re-checked.
- Public group reduced to read-only stats + games; any moderation verb
  typed in the group is deleted and the admin is redirected to DM
  (`redirectModerationToDM`). Moderation never executes publicly.
- Inline catalog trimmed: it no longer advertises moderation verbs
  (they cannot run in-chat - inline results post publicly).
- New `cmd/bidlobot-import`: streaming parser for the Desktop "Export
  chat history" JSON, seeds the `members` bucket (max() count
  semantics, idempotent). Bot stopped for a real import; `--dry-run`
  safe live.
- bbolt gained `dm_sessions`; cooldown self-evicts; cleanup Stop
  registered before render (race fix); two opus critic passes,
  must-fix holes closed.

## Outcome

DM-only moderation shipped and deployed; public timeline never carries
bot-initiated moderation. Docs realigned (scope, moderation spec,
telegram, deployment, README). Memory updated: inline is not a privacy
mechanism.
