---
id: handoff
kind: guide
---

# Handoff: next-session action plan

Last rewritten 2026-05-15, end of the cleanup-campaign-rework session.

## State (what is true right now)

- `origin/master` = `f203fc9`. Local `master` == `origin/master`
  (pushed, **in sync**, clean fast-forward, 0 ahead / 0 behind).
- Shipped in this push (prod baseline was `6942061`): evidence-graded
  `/cleanup`, the command-started cleanup **campaign** (`gracekick`),
  the owner's `/summarize` (GLM) workstream, dead immediate-kick code
  removed, docs.
- `go build ./...`, `go test ./...` (**21 packages, 0 failures**),
  `go vet`, `gofmt` all green at `f203fc9`.
- Critic: evidence-grading clean (devlog 05); campaign rework two opus
  rounds, round-2 clean; a third full pre-push opus pass over the
  entire `origin/master..master` diff (incl. the summarize-merge
  integration) = no BLOCKER / no SHOULD-FIX.
- Cleanup model now: DM `/cleanup <period>` confirm **seeds** a per-chat
  campaign (proven-stale only; never the no-evidence data gap) - it
  does NOT kick on confirm. The always-on daily scheduler then
  publicly @-tags a batch, grace `CLEANUP_GRACE` (default 72h), spares
  anyone who writes/reacts, kicks the still-silent, until the list
  empties. `/cleanup stop` cancels; re-run while active is refused.

## Negatives (NOT done - do not assume)

- **Not deployed.** Only `git push` happened. The live host
  (<deploy-host>) still runs `6942061` until someone runs the Upgrade
  steps in [70_deployment.md](70_deployment.md). Deploy is a manual
  host action; it was not requested this session.
- **Telegram behaviour not machine-verified.** The real public tag /
  real kick / "reaction or message spares" / cross-day campaign
  progression / real GLM `/summarize` are unit-tested by logic only -
  Claude cannot drive Telegram or GLM. Operator must exercise them.
- No feature auto-activates after deploy: a campaign needs an admin
  `/cleanup`; `/summarize` needs `GLM_API_KEY`.
- S4 (accepted tradeoff, not fixed): a single corrupt/unreadable bbolt
  membership row keeps a campaign non-empty, so `/cleanup` re-run is
  refused for that chat; escape is the documented `/cleanup stop`.

## Next (pick one - choices, not a queue)

- **Deploy now (~10 min).** Run the Upgrade block in
  [70_deployment.md](70_deployment.md) on the host, watch the four
  startup log lines, done. Nothing activates by itself.
- **Operator acceptance test first (~30 min, then deploy).** Use the
  Manual checklist in [40_moderation.md] / below in a test chat before
  touching prod.
- **Close the separate YouTube-si= item (~15 min).** Owner reported the
  sanitizer "reposts but doesn't delete the original". Diagnosis: the
  bot most likely lacks the **Delete Messages** admin right in that
  chat (the code reposts-then-deletes and logs+degrades by design - not
  a code bug on this branch). Verify the bot's admin rights in that
  chat; only investigate code if rights are confirmed present.

## Read order

1. `handoff.md` (this).
2. [40_moderation.md](40_moderation.md) - the campaign spec.
3. [70_deployment.md](70_deployment.md) - Upgrade + Rollback.
4. [10_scope.md](10_scope.md) - scope, the public-campaign privacy
   exception, env.
5. [devlog/06_cleanup_campaign_rework.md](devlog/06_cleanup_campaign_rework.md)
   (+ 05 for the evidence-grading rationale).
6. memory: `ux_moderation_privacy`, `bydlokod-import-workflow`,
   `project_direction`.

## Smoke test (run before touching anything)

From the repo root:

```sh
go build ./...                              # silent, exit 0
go test ./... 2>&1 | grep -c '^ok '         # -> 21
go test ./... 2>&1 | grep -E 'FAIL|panic' | grep -v 'no test'   # -> no output
go vet ./... ; gofmt -l internal/ cmd/      # both -> no output
git fetch origin && git rev-list --left-right --count origin/master...master
#   -> "0	0"  (local == origin/master == f203fc9)
```

On the deploy host, after the Upgrade block, `docker compose logs -f
bot` must show, in order: `starting build=...` ->
`authenticated bot=... can_read_all=<bool>` ->
`health server listening addr=:8080` -> `bot started, polling for
updates`. `can_read_all=false` is privacy ON and is expected.

## Agent error to log (this session)

The cleanup feature was first built with the **wrong architecture**:
two disconnected mechanisms (immediate-kick `/cleanup` + a separate
env-gated standalone daily scheduler) instead of the owner's intended
single command-started campaign. Root cause: an unconfirmed
architectural assumption - the trigger/relationship between `/cleanup`
and the daily cycle was never clarified before building. The owner
caught it ("текущий вариант хуйня"), forcing a full rework
(devlog 06). Lesson: when a feature's *trigger* and *lifecycle owner*
are ambiguous, confirm them explicitly before implementing, not after.

## Project-specific anti-patterns

1. NEVER seed/tag/kick `Preview.NoEvidence` or any
   unresolved/not-present/protected member. Proven-stale only.
2. `/cleanup` confirm must SEED, never kick immediately. Do not
   reintroduce an immediate-kick path or re-register a public cleanup
   executor (it was deliberately removed).
3. "Active since" comparisons are inclusive (`seenAtOrAfter`, `>=`) -
   Telegram timestamps are second-granular; strict `>` kicks a member
   who responded in the boundary second.
4. Never persist a `tagged` record before the public announce
   succeeded (else a kick without a delivered warning).
5. Keep `RunDaily`'s 3-phase per-chat lock: throttled kick loop OUTSIDE
   the lock; phase C re-lists so a concurrent `/cleanup stop`
   resurrects nothing.
6. Do not re-`EscapeHTML` `mention`/`UserDisplayFull` output. Telegram
   length is UTF-16 units (`utf16Len`). All public sends via the
   rate-limited `tgclient`. `cleanup.ParsePeriod` is the one period
   parser.
7. `handoff.md` is rewritten each session, never appended. Specs change
   in the same commit as the code. Devlogs are append-only.
