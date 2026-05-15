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
message/reaction, or loaded by a DM `/import` of a chat export). Unknown
@username -> actionable message pointing at import + the @username/id
distinction.

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
- **`/cleanup <period>`** - starts the **campaign** (see below); does
  NOT kick on confirm. The preview is **evidence-graded**
  (`internal/domain/cleanup`):
  - `Preview.Candidates` = members the bot *observed* (message or
    reaction, live or imported) whose last activity precedes the cutoff.
    The only set seeded into the campaign.
  - `Preview.NoEvidence` = members with zero recorded activity ever
    (join-only / react-only-before-bot). A data gap, **not** proof of
    silence: shown named for manual review, never seeded, never in
    `Candidates`.
  - Identity (Name / @handle) resolved live via `getChatMember`
    (`Service.ResolveIdentities`, bounded) - the export has no usernames
    and no name for join-only members. Left/admin/bot rows excluded.
  - `Preview.ThresholdExceedsWindow` drives a loud warning when the
    requested period exceeds the data the bot actually has.
  Empty-state copy distinguishes "no data, run import" / "everyone
  active" / "no proven-stale, only a data gap".
- **`/cleanup stop`** - `gracekick.Cancel`: drop the chat's entire
  campaign (queued + tagged). Already-kicked members are not restored.
- A second `/cleanup <period>` while a campaign is active is **refused**
  with status (no silent overwrite); stop first.

## Inactive-cleanup campaign (`internal/domain/gracekick`)

The campaign is **initiated by the admin's `/cleanup` confirm**, not by
an env flag. Confirm -> `gracekick.Seed` enqueues the proven-stale,
name-resolved `Candidates` as `queued` records (`NoEvidence` never
seeded). The always-on daily scheduler then drives the lifecycle per
chat with an active campaign, at `CLEANUP_DAILY_AT` (UTC). It is
**public by design** - an owner-approved override of the DM-only
invariant ([10_scope.md], 2026-05-15). `RunDaily` is 3-phase, per-chat
serialized (a mutex so `/cleanup stop` cannot race a tick into
resurrecting cancelled records; the throttled kick loop runs OUTSIDE the
lock so stop stays responsive):

1. **Sweep** `tagged` past `GraceDeadline`: wrote OR reacted **at/after**
   `TaggedAt` (inclusive - second-granular timestamps) -> spared,
   ticket cleared; still silent -> kicked via shared `cleanup.Service`
   (getChatMember pre-check skips now-admin/left/bot); live record
   unreadable -> deferred, never kick on uncertainty. Terminal once
   attempted; per-member kick failures logged.
2. **Promote** up to `CLEANUP_DAILY_BATCH` `queued` -> `tagged`: drop
   anyone active since `SeededAt` (spared, never tagged) or now
   admin/left/bot; unreadable -> deferred (escape: `/cleanup stop`).
   Post ONE public @-mention message stating the rule, then persist
   `tagged` (`GraceDeadline = now + CLEANUP_GRACE`, default 72h). If the
   announce send fails, records stay `queued` - a member is never
   kicked for a warning that never reached the chat.

Campaign ends when the seeded list is exhausted. The legacy immediate
public-callback kick executor was **removed** - the campaign is the only
path that bans.

Privacy-mode caveat: under BotFather privacy ON the bot does not see
ordinary messages, but does see replies to its own message and all
reactions (it is admin). The tag copy asks members to *reply or react*;
"active" is read from membership timestamps, updated from whatever the
bot may observe.

## Destructive-action safety (parity with the old public dispatcher)

Ban + cleanup carry a `pending.Action` (5-min TTL): actor-lock
(`ActorUserID == query.From.ID`), admin re-check at confirm time,
delete-before-execute (no double-tap replay), `ErrExpired` handled. A
DM is single-actor and private, so the chat-pin / forward-attack guard
the public dispatcher needed does not apply here.

## Data on ban

Warnings + stats preserved (audit). Restored view on `/unban` + rejoin.
