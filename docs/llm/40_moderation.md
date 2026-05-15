---
id: moderation
kind: spec
---

# Moderation

See also: [10_scope.md](10_scope.md), [50_telegram.md](50_telegram.md).

## Surface (revised 2026-05-15)

**Moderation runs ONLY in the private DM console.** There is no public
slash moderation. A moderation verb typed in the group is deleted and
the issuer is redirected to DM (`redirectModerationToDM` in
`routes.go`); it never executes publicly. Inline never offers
moderation (results post publicly, so inline cannot be private).

Flow: admin opens a private chat with the bot -> `/start` ->
`resolveManagedChats` (bot is admin+CanRestrict AND caller is admin)
-> 0 / 1 (auto-select) / many (inline picker) -> session stored in the
`dm_sessions` bucket -> admin issues commands against the selected
chat. All replies and errors stay in the DM; chat members see nothing.

`internal/bot/dm_console.go` + `dm_console_cleanup.go` + `dm_text.go`.

## Admin model

Source of truth: `getChatAdministrators` (Telegram API), via
`shared.AdminCache`. No bot-managed admin list.

- Cache: 60s per-chat, invalidated on `chat_member` updates.
- `requireSession` re-checks `IsAdmin` on **every** DM command; a
  demoted admin's session is cleared immediately.
- Bot self-rights: a chat only appears in `resolveManagedChats` if the
  bot is `administrator`/`creator` AND `CanRestrict`.

## Target resolution (DM has no reply-to)

`/cmd @username` -> `members.GetMemberByUsername` (case-folded scan of
the selected chat). `/cmd <numeric id>` -> used directly. There is no
reply-to in a DM, so the bot can only target users it knows (seen via
message/reaction, or loaded by `bidlobot-import`). Unknown @username ->
actionable message pointing at import + the @username/id distinction.

Validation (warn/mute/ban): bot / admin / self rejected with the
`ValidateTarget` reason surfaced verbatim.

## Commands (all in DM, responses Russian)

- **`/warn @user [reason]`** - creates a warning, returns active count.
  At 3 -> auto-mute 24h (real `restrictChatMember`). Reversible, runs
  straight through (no confirm). Counter does not reset; `/warns clear`
  to restart escalation.
- **`/warns @user`** - list active warnings (DM only - it is NOT
  world-readable in the group anymore).
- **`/warns clear @user`** - mark all `active:false` (audit preserved).
- **`/mute @user [dur]`** - `restrictChatMember`, all perms false,
  `until_date`. Durations `30m/1h/12h/Nd`, default 1h, 1m..366d.
  Permission failure -> "проверьте право ограничивать участников"
  (honest, not a misleading "try again").
- **`/unmute @user`** - restore chat default permissions.
- **`/ban @user [reason]`** - **requires in-DM confirm** (irreversible
  for the member). Pending action, `[✅ Подтвердить] [✕ Отмена]` in the
  DM. On confirm: actor-lock + admin re-check, then `banChatMember`.
  On failure the dead keyboard is stripped.
- **`/unban @user`** - pre-check `getChatMember` status, then
  `unbanChatMember(only_if_banned:true)` (mandatory flag - without it
  the call removes active members).
- **`/cleanup <period>`** - see [10_scope.md] + dm_console_cleanup.go.
  Confirm in DM; preview distinguishes "no data, run import" from
  "everyone active"; kick loop has a working Stop button registered
  before render; per-chat mutex blocks a second admin's concurrent run.

## Destructive-action safety (parity with the old public dispatcher)

Ban + cleanup carry a `pending.Action` (5-min TTL): actor-lock
(`ActorUserID == query.From.ID`), admin re-check at confirm time,
delete-before-execute (no double-tap replay), `ErrExpired` handled. A
DM is single-actor and private, so the chat-pin / forward-attack guard
the public dispatcher needed does not apply here.

## Data on ban

Warnings + stats preserved (audit). Restored view on `/unban` + rejoin.
