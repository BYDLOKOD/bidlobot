---
id: handoff
kind: guide
---

# Handoff: next session action plan

Last updated: 2026-05-15, after the load/correctness audit + three
fixes, shipped and deployed to <deploy-host> (commit `be90a00`).

## Current state

Branch: `master`, pushed. `go test -race ./...` green (~250 tests,
57.7% stmt). Image builds; ships `bidlobot` + `bidlobot-backup` +
`bidlobot-probe` + `bidlobot-import`. Deployed container healthy,
polling.

Load audit (30+ active users) found and fixed three hot-path defects
(commit `be90a00`):

- **Rate limiter now covers the public path.** Games, `/stats`, help,
  onboarding and the mod-redirect previously sent via the raw
  `*telego.Bot`, bypassing the per-chat 15/min budget + retry. They
  now go through `tgclient` (`App.sender` is a required `NewApp`
  param; games via `GamesSender`, stats via `MessageSender`;
  `Client.SendDice` added). DM console / inline-answer / v1 callback
  dispatcher stay raw by design (single-chat / no-chat-id / per-tap).
- **cooldown init race fixed.** Eager-init in `NewApp`; lazy nil-check
  removed. `-race` test added.
- **`go bh.Start()` zombie fixed.** Error surfaced via a select so the
  app shuts down instead of hanging silently.

Architecture after the rework:

- **Moderation + cleanup = DM console only** (`internal/bot/dm_console*.go`).
  Admin DMs the bot, `/start` -> picks a managed chat (resolved as: bot
  is admin+CanRestrict AND caller is admin), then `/warn /warns /mute
  /unmute /ban /unban /cleanup /stats`. Ban + cleanup confirm in-DM.
  Per-chat cleanup mutex; Stop button registered before render (no
  silent-abort race); cooldown map self-evicts.
- **Public group = read-only + games**. `/stats` + `/dice /battle
  /quiz` (per-user cooldown). Any moderation verb in the group ->
  `redirectModerationToDM`: deletes the command, DMs the admin.
  Moderation never executes publicly.
- **Inline** moderation verbs -> generic "use DM" hint, no pending
  (inline is not private; that premise was wrong).
- **History bootstrap + cleanup operating model**: `cmd/bidlobot-import`
  streams a Telegram Desktop "Export chat history" JSON into the members
  bucket so `/cleanup` works on pre-bot history. Bot must be stopped for
  a real import (bbolt lock); `--dry-run` is safe live. **Privacy mode
  does NOT need disabling** - recommended model is periodic &
  import-driven: keep `/setprivacy` ON, run "fresh export -> import ->
  `/cleanup` immediately", keep the bot admin so reactions still flow.
  Disable privacy only for continuous live message stats. Full rationale
  + false-positive corner cases in `35_history_import.md` "Operating
  model". `cleanup` is human-in-the-loop (preview+confirm +
  ObservationWindow) precisely because import data is partial.
- Onboarding message on bot promotion. All user copy Russian.

## What does NOT exist / known follow-ups

- Fresh-deploy reconcile: if the bot misses its own `my_chat_member`
  promotion (was down during promotion, or added pre-build), the chat
  is unregistered and `/start` says "re-grant me admin". No
  getChat-based backfill (Bot API has no chat enumeration). Normal
  flow (deploy -> add bot) observes the event fine.
- `moderation.Service.resolveUsername` is a stub returning "" -> warning
  lists show issuer as `user_<id>`. Cosmetic.
- Public `/ban` has an unavoidable ~200-500ms visibility window before
  the redirect deletes it (delete-after-the-fact). Onboarding trains
  admins away from this.
- **Prod is privacy ON** (`getMe can_read_all=false`, verified
  2026-05-15). This is an operating *choice*, not a bug: live message
  stats are limited; cleanup works via the periodic import model. Only
  a BotFather flip + re-add (operator action, not code) changes it.
- **FINDING #3 (deferred, not a correctness bug):** membership writes
  one synchronous fsync'd bbolt txn per event (stats is buffered, this
  is not). Bounded by telego's 100-deep update channel; optimize later
  via `db.Batch()` if a very high-traffic chat needs it.
- **Test reality:** ~250 component/unit tests, `-race` green; strong
  per-component coverage incl. the new sender-routing + cooldown-race
  regressions. NOT covered: full-stack replay through the real telego
  router; `testdata/session{1,2}.jsonl` are stale Clojure/profiles-era
  fixtures (the replay test only feeds the membership domain, asserts
  little). No recording of real current-bot traffic exists (server
  `RECORD_UPDATES` unset). The legacy `~/org/legacy/chat-export.org`
  counting methodology was NOT cross-checked against the implemented
  `LastMessageAt OR LastReactionAt` rule (operator declined the read).

## Immediate next steps (options)

1. getChat-based chat reconcile on `/start` when 0 managed chats, to
   close the fresh-deploy edge.
2. Implement `resolveUsername` (issuer display in warning lists).
3. MTProto member sync (`channels.getParticipants`) to catch ghost
   members never seen by the bot or export.
4. Enable `RECORD_UPDATES` (compose bind-mount) to capture real
   current-bot traffic, then replace the stale `testdata/session*`
   fixtures and add a full-stack replay through the real router.
5. `db.Batch()` for the per-event membership write (FINDING #3) if a
   high-traffic chat warrants it.

## Manual verification in @testovaya22222222222222222222

Claude cannot click in Telegram. The operator must run this in the
deployed test group + DM. Order:

1. **Onboarding**: remove+re-add the bot as admin -> group shows the
   one-time "BidloBot подключён ... модерация в личке" message.
2. **DM start**: open DM with the bot, `/start` -> it auto-selects
   "Тестовая" (single managed chat) and prints the command help.
3. **Public moderation is intercepted**: in the group type
   `/ban @someone`. The command is deleted; you get a DM telling you
   to use the console. Nothing executes in the group.
4. **Warn in DM**: `/warn @member testing` -> DM confirms, group sees
   nothing. `/warns @member` -> list in DM.
5. **Ban confirm**: `/ban @member spam` -> DM shows confirm buttons.
   Tap ✕ -> cancelled. Repeat, tap ✅ -> member banned, DM confirms.
   From a second account, tap a stale confirm -> "только инициатор".
6. **Cleanup empty-state**: `/cleanup 6mo` on the fresh chat -> DM says
   "нет данных" with the import instructions (NOT "all active").
7. **Import + cleanup**: export the group history (Telegram Desktop ->
   ⋯ -> Export chat history -> JSON). On the server:
   `docker compose stop bot`; `docker compose run --rm -v
   /path/result.json:/tmp/r.json bot bidlobot-import --json /tmp/r.json
   --chat-id -1009000002`; `docker compose start bot`. Then DM
   `/cleanup 6mo` -> preview lists stale members -> tap ✅ -> progress
   with a working ✕ Остановить button.
8. **Games + cooldown**: `/dice` works; spam `/dice` 5× fast -> only
   the first lands (cooldown). `/stats top` in group works.
9. **Health**: `docker inspect -f '{{.State.Health.Status}}'
   bidlobot` -> `healthy`.

## Read before starting

- `docs/llm/10_scope.md` (command surfaces, revised)
- `docs/llm/35_history_import.md`
- `internal/bot/dm_console.go` + `dm_console_cleanup.go`
- `internal/bot/routes.go` (registerRoutes, redirectModerationToDM)
- memory: telegram-api-constraints (inline-not-private), ux-moderation-privacy

## Anti-patterns

1. Do NOT reintroduce public moderation commands or advertise them in
   `helpSupergroup` / `setCommands` group scope.
2. Inline is NOT private - never route a destructive action through it.
3. `bidlobot-import` needs the bot stopped (bbolt exclusive lock).
4. Cleanup Stop button must be registered (`cleanupRuns.start`) BEFORE
   the button is rendered.
5. telego API method gotchas unchanged (see prior handoff history /
   60_architecture.md).
