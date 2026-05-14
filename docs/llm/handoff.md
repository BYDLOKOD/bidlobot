---
id: handoff
kind: guide
---

# Handoff: next session action plan

Last updated: 2026-05-15, after independent critic review and production
hardening of the deploy bundle. Bot is dockerized and ready for deploy
to <deploy-host>.

## Current state

Branch: `master`. Domains: stats, moderation, cleanup, pending,
membership, games (dice / battle / quiz). All wired through the
production tgclient wrapper (rate-limit + retry + migration). End-to-end
in-process tests cover inline -> dispatcher -> executor -> bbolt. `go
test -race ./...` green across 14 packages.

Production posture (post-critic):

- Dockerfile: multi-stage, alpine 3.20 runtime, tini PID 1, USER 65532
  (non-root), 30 MB image, ships `bidlobot`/`bidlobot-backup`/`bidlobot-probe`.
- docker-compose.yml: single replica (Telegram allows only one
  `getUpdates` poller per token), `restart: unless-stopped`, internal
  healthcheck (no host port published), 30s `stop_grace_period`,
  256 MB / 0.5 CPU resource caps, JSON log rotation 10 MB x 5.
- `/health` checks: bbolt no-op view txn (real liveness, not the prior
  `Path() != ""` tautology), update freshness with 5 min window, cached
  `getMe` (60 s TTL so brief Telegram 5xx don't bounce the container).
- Volume permission: empty `/var/lib/bidlobot/.keep` baked into the
  image with `bidlobot:bidlobot` ownership so a fresh named volume
  inherits 0750 instead of root:root 0755.
- `deploy/backup.sh`: host-side stop -> cp -> start. Trades ~10s
  downtime for a guaranteed-consistent bbolt snapshot. Failed backups
  exit nonzero so cron alerts.
- `cmd/demo` and `cmd/smoke` removed: the test-time helpers became a
  liability (hardcoded chat ids; same-token race against prod).

## BotFather one-time setup

In @BotFather, against the deployed bot token:

1. `/setprivacy` -> bot -> Disable. **After this you must remove and
   re-add the bot to the chat** (privacy is cached at join time).
2. `/setinline` -> bot -> placeholder text such as
   `stats top, cleanup 6mo, warn @user`.
3. `/setinlinefeedback` -> Disabled.
4. Confirm with `docker exec bidlobot bidlobot-probe` -- expects
   `can_read_all=true, supports_inline=true`.

## Manual smoke checklist (production)

Run inside the deployed chat:

1. **Bot identity** -- `docker compose logs bot | head -20` must show
   `authenticated bot=<name> can_read_all=true supports_inline=true`.
2. **Help** -- `/help` -> bot replies.
3. **Stats baseline** -- `/stats` -> overview. May be 0 entries on a
   fresh deploy.
4. **Stats grows** -- write 3-5 plain messages, wait 60 s for the flush
   window. `/stats top` lists you.
5. **Reaction tracking** -- react with any emoji to a recent message.
   `/cleanup 1d` should NOT list you because the membership row's
   `LastReactionAt` is fresh.
6. **Inline read-only** -- `@<bot> stats top` -> carousel offers
   options. Tap "/stats top" -> bot replies as if you typed the
   slash command.
7. **Inline destructive (cancel)** -- `@<bot> warn @<member> testing`
   -> tap preview -> [Подтвердить] [Отмена] keyboard. Tap Cancel ->
   message edits to "Действие отменено", buttons cleared.
8. **Inline destructive (apply)** -- repeat but Подтвердить ->
   message edits to "@<user> предупреждён (1/3)". Re-tapping must
   alert "Действие не найдено или уже выполнено".
9. **Inline guard: actor mismatch** -- from a second account, try to
   tap Подтвердить on someone else's pending -> alert "Только
   инициатор...". Pending unaffected.
10. **Inline guard: chat pinning** -- start a destructive in chat A,
    forward the result to chat B, tap Подтвердить -> alert "Эта
    команда привязана к другому чату".
11. **Cleanup empty path** -- `@<bot> cleanup 1d` -> "Кандидатов
    нет". Pending deleted.
12. **Cleanup populated path** -- requires a chat with at least one
    inactive member and threshold > 24 h. Preview lists candidates;
    "Кикнуть всех" -> "Чистка запущена" -> progress edits -> final
    report. **Test in a throwaway chat first.**
13. **Healthcheck** -- `docker inspect -f '{{.State.Health.Status}}'
    bidlobot` -> `healthy`.
14. **Graceful shutdown** -- `docker compose stop bot`. Logs show
    "shutdown signal received" -> "handler stopped, stats flushed"
    within 30 s.

## What does NOT exist

- Webhook deployment path (long-polling only).
- Branch protection or required reviews on the public repo (operator
  task on github.com after first push).
- Secret scanning in CI is added (gitleaks step) but no actions are
  pinned to commit SHAs yet -- low priority for a single-maintainer
  repo, raise if external contributors appear.
- HTTPS / reverse proxy in front of the health endpoint (intentionally
  not published to the host; reach from inside compose if needed).

## Deploy runbook

1. `gh repo create veschin/bidlobot --public --source . --remote origin --push`
   (already done if `git remote -v` shows origin pointing to GitHub).
2. `scp` the repo or `git clone` to `/opt/bidlobot` on the deploy host.
3. `cp deploy/env.example /opt/bidlobot/env`, set `TG_BOT_TOKEN`.
4. `cd /opt/bidlobot && docker compose up -d --build`.
5. `docker compose logs -f bot` until `bot started, polling for updates`.
6. Run the manual smoke checklist above.
7. (Optional) Add `/etc/cron.d/bidlobot-backup` per `deploy/backup.sh`
   header.

## Anti-patterns (carry-over)

1. **telego methods require `context.Context` as first arg.**
2. **`Predicate` signature:** `func(ctx, update) bool`.
3. **`Use()` takes Handler.** Middleware = Handler that calls `ctx.Next`.
4. **`MemberUser()` returns value, not pointer.** Cannot compare to nil.
5. **`ChatPermissions` fields are `*bool`.** Must pass pointers.
6. **Stats counting must be middleware**, not handler. telego routes to
   first match only.
7. **Bot API has no `getChatMembers`.** Member list is bottom-up from
   observed events.
8. **`message_reaction` requires bot admin + explicit `allowed_updates`.**
9. **`InlineQuery` carries no `chat_id`.** Defer admin checks to
   callback after preview is selected.
10. **Inline result is sent as the user's message.** Callback handlers
    run with `query.From` = user, `query.Message.Chat.ID` = real chat.
11. **Kick = `banChatMember` then `unbanChatMember(only_if_banned=true)`.**
    Without unban the user is permanently banned.
12. **Cleanup preview declares the observation window.** The bot only
    knows users it has observed; users silent before bot install are
    invisible to cleanup. Always include this in the preview message.
13. **Single getUpdates poller per token.** Two processes with the same
    token race on Telegram's 409 Conflict, silently splitting traffic.
    Stop production before any local run.
