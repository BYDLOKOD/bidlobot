---
id: devlog-06
kind: devlog
date: 2026-05-15
---

# 06 - Cleanup reworked into a command-started campaign

Branch `feat/cleanup-rework` (worktree). Follows devlog 05.

## Why

Devlog 05 shipped two disconnected things: DM `/cleanup` = immediate
ban+unban on confirm, plus a separate env-gated (`CLEANUP_DAILY_ENABLED`)
standalone daily scheduler that self-selected candidates. The owner's
actual model, confirmed this session: `/cleanup` is the **initiator** of
the daily public lifecycle - confirm seeds a campaign with that list;
the bot then tags/graces/kicks day by day until the list is exhausted.
The old split was rejected ("текущий вариант хуйня").

## What changed

- **gracekick = campaign engine.** `Record` gains `State`
  (queued|tagged) + `SeededAt`. `Seed(absChatID, members, now)` enqueues
  proven-stale members as `queued`. `RunDaily` no longer self-selects:
  it sweeps `tagged` past grace (save on msg/reaction at-or-after
  `TaggedAt`, else kick) and promotes up to `Batch` `queued`->`tagged`
  (drop anyone active since `SeededAt` or now admin/left/bot, announce
  one public message, persist). `Cancel` = `/cleanup stop`.
  `CampaignSize` gates re-runs.
- **DM `/cleanup`.** Confirm -> `startCleanupCampaign` -> `Seed` (NOT a
  kick). `/cleanup stop` cancels. Re-run while active is refused.
  Identities resolved at seed so the public tag is readable.
- **Scheduler always on**, no enable flag; idle until a campaign
  exists. Config: dropped `CLEANUP_DAILY_ENABLED` + `CLEANUP_THRESHOLD`
  (period is the `/cleanup` arg); kept `CLEANUP_DAILY_AT/GRACE/BATCH`.
- **Removed dead destructive code**: the legacy in-chat
  `CleanupExecutor` (immediate ban+unban, still registered on the public
  dispatcher), `cleanupRuns`, the `dm:abort` verb. The campaign is now
  the only path that bans.

## Decisions / fixes from two opus-critic rounds

- **B1 (round-1 BLOCKER, fixed):** second-granular Telegram timestamps
  meant a member acting in the exact second of `TaggedAt` was kicked
  despite responding. Comparison is now inclusive (`seenAtOrAfter`,
  `>=`), for both the sweep-save and the promote-spare. Regression test
  added.
- **S5 (fixed):** a per-chat mutex (`sync.Map`) serializes
  Seed/Cancel/RunDaily so `/cleanup stop` can't race a tick into
  resurrecting cancelled records. The throttled kick loop runs OUTSIDE
  the lock (3-phase RunDaily) so stop stays responsive; phase C
  re-lists, so a stopped campaign resurrects nothing.
- **S3 (fixed):** per-member kick failures and post-kick delete
  failures are now logged.
- **S4 (accepted tradeoff):** a permanently-unreadable `queued` row
  (one corrupt bbolt membership row) keeps the campaign non-empty and
  blocks re-run; escape is the documented `/cleanup stop` (the refusal
  copy says so). Auto-dropping an unverifiable member would be the
  wrong fix on a destructive path. Optional future: stuck-row age cap.

Round-2 critic: no BLOCKER / no SHOULD-FIX; locking traced sound.

## Verification

`go build ./...`, `go test ./...` (21 pkg), `go vet`, `gofmt` green.
Not machine-verifiable here: the real Telegram public tag / kick /
reaction-clears-grace and the cross-day campaign progression - operator
must exercise in the test chat (handoff checklist).
