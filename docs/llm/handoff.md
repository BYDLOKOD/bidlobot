---
id: handoff
kind: guide
---

# Handoff: next session action plan

Last updated: 2026-05-15, after the cleanup-rework session.

Prior baseline (monthly-stats / mini-games / YouTube-si= / DM-import /
cooldown-UX) is on `origin/master`, deployed to prod (<deploy-host>) at
commit `6942061`, container healthy (`e2e_test_bot`). Local `master`
now carries **two further workstreams merged and not yet pushed**:
cleanup-rework + chat-summarization (GLM).

## Branch / git topology (read before any git op)

- Local `master` HEAD `8c17737` = `origin/master` + cleanup commit
  (`2e36426`) + `feat/summarize-glm` (2 commits) via a `--no-ff` merge.
  **4 ahead of `origin/master`, 0 behind. Not pushed.**
- `feat/summarize-glm` was developed by the owner in parallel (its own
  worktree `.claude/worktrees/summarize-glm`); merged here non-rewriting
  (branch + worktree intact). The summarize WIP that an earlier step
  parked in a stash was already recovered and committed by the owner
  into `9ba9f18`/`58c8feb` - **no stash to restore, nothing lost.**
- Merge conflicts (config.go, deploy/env.example, docs 00_index /
  10_scope; the cleanup rebase also touched handoff) were all additive
  and resolved by union - no logic contradiction, build/tests green.
- cleanup work lives in worktree `.claude/worktrees/cleanup-rework`
  (branch `feat/cleanup-rework`); safe to remove after push.

## Current state

`go build ./...` green; `go test ./...` green (21 pkg, incl. summarize +
glm); `go vet` and `gofmt` clean on the merged tree. Two opus-critic
rounds on the cleanup work: one BLOCKER (B1: unresolved
member could be publicly tagged/kicked) + S1/S2/S3 + a follow-up
BLOCKER (UTF-16 vs rune message budget) - all resolved with regression
tests. N2 (no shared per-chat lock between the daily scheduler and DM
`/cleanup`) deliberately deferred (critic-rated acceptable: the
getChatMember pre-check converges a double-kick to a skip).

Added this session (full detail:
`devlog/05_cleanup_evidence_grading_and_daily_lifecycle.md`):

- **Evidence-graded cleanup** (`internal/domain/cleanup`):
  `Preview.Candidates` (observed-then-silent, actionable) vs
  `Preview.NoEvidence` (never observed - data gap, never auto-kicked);
  `ResolveIdentities` (live Name/@handle via getChatMember, bounded,
  flags left/admin/bot); `ThresholdExceedsWindow` warning;
  `cleanup.ParsePeriod` is now the one period parser. DM `/cleanup`
  preview rewritten (grouped, named, honest empty states; confirm
  kicks proven-stale only).
- **Daily lifecycle** (`internal/domain/gracekick` + `gracekick` bbolt
  bucket + `App.runDailyCleanup` scheduler + `CLEANUP_DAILY_*` config):
  opt-in, OFF by default; public tag -> 3-day grace -> kick; saved by
  message OR reaction; proven-stale only; announce-fail persists no
  ticket; affirmative-safety pick filter; UTF-16-bounded one-message
  announce; inFlight-tracked scheduler.

## Known follow-ups / limitations (documented, not silent)

1. **Daily lifecycle reverses the DM-only privacy invariant** - owner
   decision, scoped by opt-in + proven-stale-only + batch cap + grace.
   Recorded in `10_scope.md` / `40_moderation.md`;
   `ux_moderation_privacy` memory now has the documented exception.
2. **Privacy-mode caveat.** Under BotFather privacy ON the bot does not
   see ordinary messages; it sees reactions and replies-to-itself. The
   tag copy asks members to reply or react; "reappeared" reads
   membership timestamps, which update from whatever the bot may see.
3. **Restart-window double-run.** A restart in the narrow window around
   `CLEANUP_DAILY_AT` can run the daily tick twice; the lifecycle
   absorbs it (sweep only acts past a deadline, tag skips ticketed
   members, batch-capped). No restart-persistence by design.
4. **N2 (deferred):** no shared per-chat lock between the daily
   scheduler and DM `/cleanup`. Concurrent runs on one chat are
   redundant API, not a wrong-kick (getChatMember pre-check). Add a
   shared chat-claim if it ever matters.

## Immediate next steps

1. **Push + deploy.** Local `master` (`8c17737`) = origin/master +
   cleanup + summarize merge, 4 ahead / 0 behind, green. Owner to
   confirm `git push origin master` (prod is live), then standard
   deploy. Both heavy features are OFF by default: the daily lifecycle
   needs `CLEANUP_DAILY_ENABLED`; `/summarize` needs `GLM_API_KEY`.
   Enabling either is a separate, explicit step.
2. **Operator manual verification** in the test chat (Claude cannot
   drive Telegram). See below.
3. Separate item the owner flagged: the YouTube si= sanitizer "does not
   delete the original, only reposts". Diagnosis: `handleSanitize`
   reposts then deletes; if the original survives, `DeleteMessage` is
   failing - almost certainly the bot lacks the **Delete Messages**
   admin right in that chat (logged, defensive by design). Verify the
   bot's admin rights; not a code bug on this branch.

## Manual verification (operator must run; Claude cannot click Telegram)

In the test chat (bot = admin with restrict + delete rights) + a DM:

1. DM `/cleanup 6mo` after an import: names/@handles show; two groups
   ("молчат давно" vs "активность не зафиксирована"); the loud window
   warning fires when 6mo exceeds the data window; confirm kicks only
   the proven-stale group.
2. Enable `CLEANUP_DAILY_ENABLED=true` + a near-future `CLEANUP_DAILY_AT`
   in the test chat. Confirm: one public message tags proven-stale only
   (never the no-evidence members); a tagged member who reacts or
   writes is spared at the next tick; one who stays silent past
   `CLEANUP_GRACE` is kicked; the message is not re-posted for members
   still inside their grace window.
3. `--check-config` rejects a bad `CLEANUP_DAILY_AT` / threshold /
   grace / batch when enabled.

## Read before starting

- `docs/llm/40_moderation.md` (cleanup grading + daily lifecycle)
- `docs/llm/10_scope.md` (privacy-invariant exception, env vars)
- `docs/llm/35_history_import.md` ("Resolved 2026-05-15" note)
- `docs/llm/devlog/05_cleanup_evidence_grading_and_daily_lifecycle.md`
- memory: `bydlokod-import-workflow`, `project_direction`,
  `ux_moderation_privacy`

## Anti-patterns

1. NEVER auto-tag or auto-kick `Preview.NoEvidence`, nor any
   unresolved/not-present/protected member. Publicly tagging a member
   the bot has no evidence against is the exact failure this session
   fixed. The gracekick pick filter is affirmative-safety only.
2. Do NOT claim "everyone active" when only a data gap exists - the
   empty-state copy distinguishes the three cases on purpose.
3. Do NOT re-`EscapeHTML` `UserDisplayFull` / `mention` output
   (double-escape); they are already HTML-safe.
4. Do NOT persist a grace ticket before the public announcement
   succeeded (else a member is kicked for a warning never delivered).
5. Keep `cleanup.ParsePeriod` the single period parser.
6. Telegram message limits are UTF-16 code units, not runes - keep the
   `utf16Len` budget in `gracekick.fitOneMessage`.
7. All public sends through the rate-limited `tgclient` wrapper.
