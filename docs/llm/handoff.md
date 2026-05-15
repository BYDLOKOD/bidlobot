---
id: handoff
kind: guide
---

# Handoff: next session action plan

Last updated: 2026-05-15, after the cleanup-campaign-rework session.

Prior baseline (monthly-stats / games / YouTube-si= / DM-import /
cooldown-UX) is on `origin/master`, deployed to prod (<deploy-host>) at
commit `6942061`, container healthy (`e2e_test_bot`). Local `master`
carries **three workstreams merged, not yet pushed**: evidence-graded
cleanup, the cleanup **campaign rework**, and chat-summarization (GLM).

## Branch / git topology (read before any git op)

- Work is in worktree `.claude/worktrees/cleanup-rework`
  (`feat/cleanup-rework`). Local `master` is fast-forwarded from it.
  **Ahead of `origin/master`, NOT pushed.** Push is the only remaining
  outward step and needs owner confirmation (prod is live).
- `feat/summarize-glm` (owner's parallel workstream) was merged
  `--no-ff`, branch/worktree intact. Earlier parked stash was already
  recovered+committed by the owner - nothing lost (audited).
- Doc/config merge conflicts were additive, resolved by union.

## Current state

`go build ./...`, `go test ./...` (21 pkg), `go vet`, `gofmt` all green.
**Two opus-critic rounds on the rework**: round-1 BLOCKER B1
(same-second-as-tag activity could kick a responsive member) + S3/S5
fixed with regression tests; round-2 = clean (no BLOCKER / no
SHOULD-FIX), locking traced sound. Earlier evidence-grading critic pass
(devlog 05) also clean.

The model now (full detail: `devlog/06_cleanup_campaign_rework.md`,
spec: `40_moderation.md`):

- DM `/cleanup <period>`: evidence-graded preview (proven-stale vs
  no-data). Confirm **does NOT kick** - it `gracekick.Seed`s the
  proven-stale list as a per-chat **campaign** (`queued`). `NoEvidence`
  never seeded.
- The always-on daily scheduler drives the campaign per chat:
  promote `Batch` `queued`->`tagged` (one public @-tag message, grace
  `CLEANUP_GRACE` default 72h), spare anyone who wrote/reacted, kick the
  still-silent after grace, until the seeded list is exhausted.
- `/cleanup stop` cancels the chat's campaign; re-running `/cleanup`
  while active is refused.
- `RunDaily` is 3-phase, per-chat mutex; throttled kick loop runs
  outside the lock so `/cleanup stop` stays responsive.
- The legacy immediate-kick `CleanupExecutor` (+`cleanupRuns`,
  `dm:abort`) was **removed** - the campaign is the only ban path.
- Config: no enable flag; `CLEANUP_DAILY_ENABLED`/`CLEANUP_THRESHOLD`
  dropped; `CLEANUP_DAILY_AT`/`GRACE`/`BATCH` only tune pacing.

## Known follow-ups / limitations (documented, not silent)

1. **Campaign is PUBLIC by design**, an owner-approved override of the
   DM-only invariant - but now strictly command-initiated (no
   autonomous trigger). `10_scope.md` / `40_moderation.md` /
   `ux_moderation_privacy` memory record the exception.
2. **Privacy-mode caveat.** Privacy ON: bot sees reactions + replies to
   itself, not ordinary messages. Tag copy asks reply-or-react;
   "active" read from membership timestamps.
3. **S4 (accepted):** a permanently-unreadable `queued` row (one corrupt
   bbolt membership row) keeps the campaign non-empty -> `/cleanup`
   re-run refused. Escape is the documented `/cleanup stop` (refusal
   copy says so). Optional future: stuck-row age cap.
4. **Restart window:** a restart around `CLEANUP_DAILY_AT` may double-
   tick; absorbed (sweep only past deadline, promote skips ticketed,
   batch-capped). No restart-persistence by design.
5. `/summarize` (GLM) is OFF unless `GLM_API_KEY` set.

## Immediate next steps

1. **Push + deploy.** Local `master` is ahead of `origin/master`,
   green. Owner confirms `git push origin master` (prod live), then
   standard deploy. No feature auto-activates: a campaign needs an admin
   `/cleanup`; `/summarize` needs `GLM_API_KEY`.
2. **Operator manual verification** (Claude cannot drive Telegram) -
   below.
3. Owner-flagged separate item: YouTube si= sanitizer "doesn't delete
   original" - almost certainly the bot lacks the **Delete Messages**
   admin right in that chat (code logs + degrades by design). Verify
   bot rights; not a code bug on this branch.

## Manual verification (operator must run)

Test chat, bot = admin with restrict + delete + (ideally) low
`CLEANUP_DAILY_AT` and small `CLEANUP_GRACE` for a fast loop:

1. DM `/cleanup 6mo` after an import: names/@handles show; two groups
   ("молчат давно" vs "активность не зафиксирована"); window warning if
   6mo > data; confirm replies "запущено N", **no immediate kick**.
2. Wait for the daily tick: ONE public message @-tags up to `BATCH`
   proven-stale (never the no-evidence group). A tagged member who
   writes/reacts is spared at the next tick; a still-silent one is
   kicked after `CLEANUP_GRACE`; batches continue until the list ends.
3. `/cleanup` again while active -> refused with status. `/cleanup stop`
   -> campaign cleared.
4. `--check-config` rejects a bad explicit `CLEANUP_DAILY_AT` /
   `CLEANUP_GRACE` / `CLEANUP_DAILY_BATCH`.

## Read before starting

- `docs/llm/40_moderation.md` (campaign spec)
- `docs/llm/10_scope.md` (privacy exception, env)
- `docs/llm/devlog/06_cleanup_campaign_rework.md` (+ 05 for grading)
- memory: `bydlokod-import-workflow`, `project_direction`,
  `ux_moderation_privacy`

## Anti-patterns

1. NEVER seed/tag/kick `Preview.NoEvidence` or any
   unresolved/not-present/protected member. Proven-stale only.
2. `/cleanup` confirm must SEED, never kick immediately. Do not
   reintroduce an immediate-kick path or re-register a public
   cleanup executor.
3. Timestamp comparisons for "active since" are inclusive (`>=`,
   `seenAtOrAfter`) - second-granular Telegram ts; strict `>` kicks a
   member who responded in the boundary second.
4. Never persist a `tagged` record before the public announce
   succeeded (else a kick without a delivered warning).
5. Keep `RunDaily`'s 3-phase locking: kick loop OUTSIDE the per-chat
   mutex, phase C re-lists so a concurrent stop resurrects nothing.
6. Do not re-`EscapeHTML` `mention`/`UserDisplayFull` output.
7. Keep `cleanup.ParsePeriod` the single period parser; Telegram length
   is UTF-16 units (`utf16Len`); all public sends via rate-limited
   `tgclient`.
