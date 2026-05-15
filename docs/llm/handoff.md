---
id: handoff
kind: guide
---

# Handoff: next-session action plan

Last rewritten 2026-05-16, end of the privacy-leak + owner-feedback
session. The cleanup-campaign baseline (2026-05-15) below is unchanged
and still current; this session added 2 LOCAL commits on top.

## State (what is true right now)

- `origin/master` = `f203fc9` (NOT moved this session). Local `master`
  is **2 commits AHEAD, 0 behind, NOT pushed**:
  - `513b8d5` chore(privacy): purge real PII from testdata + handoff.
  - `<next>` the 3 bug fixes + S1 + game-content expansion + specs +
    devlog 07 (the commit this handoff ships with).
- Nothing pushed, nothing deployed (owner explicitly withheld both;
  redeploy is coordinated on the owner's return).
- Shipped earlier (prod baseline `6942061`, on `origin/master`):
  evidence-graded `/cleanup`, the command-started cleanup **campaign**
  (`gracekick`), `/summarize` (GLM), docs.
- `go build ./...`, `go test ./...` (**21 packages, 0 failures**),
  `go vet`, `gofmt` all green at local HEAD (re-run, uncached).
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

## Fixed this session (owner-reported 2026-05-16; committed LOCAL only)

All in the `<next>` commit, full suite green, opus-critic-reviewed
(1 BLOCKER refuted with evidence + 3 items resolved). NOT deployed.

1. **Stats `@`-mention -> inert.** `shared.UserDisplay` /
   `UserDisplayFull` now render the handle WITHOUT `@` (and never a
   `tg://user?id=` / `text_mention`); `resolveQuipTarget` too. Fixes
   `/stats*`, monthly nominations, all games, the YouTube attribution
   header. gracekick's own `mention()` is untouched (the sanctioned
   notifier). `30_stats.md` corrected (it had mandated `@username`).
2. **Import-only name no longer leaks the operator's contacts.**
   `membershipDisplayResolver` blanks `FirstName` for
   `KnownVia == SourceImport` -> caller renders neutral `User <id>`;
   self-heals on live write. **S1 (critic):** `KnownVia` precedence
   made monotonic in `storage.UpsertMember` so the owner's planned
   BYDLOKOD **re-import will NOT downgrade** a healed member back.
   `35_history_import.md` corrected.
3. **`/duel` validates membership.** `DuelHandler` got a `duelMembers`
   dep; the opponent must resolve via `GetMemberByUsername` in THIS
   chat or the duel is refused (no dice, inert message - a lurking
   member is not pinged even by the rejection). Product Q resolved
   conservatively: the duel does NOT notify the challenged member
   (inert, per the invariant); owner may revisit.
4. **Game content expanded** (replayability): 8ball 20->~65, roast/
   praise 15->40 each, hangman 44->156 (+ a full-pool invariant test),
   trivia 26->46, snippets 12->23 (verified). Deliberately quality/
   correctness-first, NOT a literal 20x on the Q&A pools (wrong facts
   are worse than fewer questions) - owner can ask for more volume.

## Privacy / personal-data leak (owner directive 2026-05-16)

Owner: "в апстрим репу пролезло много личных данных" - he already
deleted a test-data folder that contained his bio. Directive: audit the
working tree AND git history for all personal-data leaks (bio, real
names, the user's handle/ids, the БЫДЛОКОД export, emails, phones,
secrets/keys, hardcoded personal chat ids), clean the working tree, and
**prepare** (do not autonomously execute) a `git filter-repo` history
scrub + force-push. The force-push to `origin` is the one coordinated
action left for the owner's return ("когда я приду ... мы передеплоим");
a backup bundle is taken before any rewrite. Any *live* secret found in
history must be **rotated** by the owner regardless of scrub (a pushed
secret is already compromised). Full audit + the dry-run scrub runbook:
[devlog/07_privacy_leak_audit.md](devlog/07_privacy_leak_audit.md).
Status: working tree sanitized + committed locally (`513b8d5`, not
pushed); the git-filter-repo command was dry-run on a LOCAL mirror (0
residual PII, 21 packages green) - corroborating, NOT a substitute for
the owner's own step-4 verify on the origin mirror before the
irreversible push. Two MANDATORY owner actions remain, both
owner-only: (a) rotate `TG_BOT_TOKEN` (@BotFather) + `GLM_API_KEY`
(z.ai) NOW - not a git problem, the keys are already burned via a chat
transcript; (b) run devlog-07 steps 1-6 to scrub history and
force-push (coordinated, irreversible).

## Next (owner actions on return - ORDERED, not a free choice)

1. **Rotate creds first (~5 min, owner-only, do before anything).**
   `TG_BOT_TOKEN` via @BotFather, `GLM_API_KEY` via z.ai. Independent
   of git; the keys are already exposed via a chat transcript.
2. **Scrub history + force-push (coordinated, irreversible).** Follow
   [devlog/07_privacy_leak_audit.md](devlog/07_privacy_leak_audit.md)
   steps 1-6. Step 4 (residual-PII grep == 0 on the origin mirror) is
   gating; do not skip it on the strength of the dry-run.
3. **Then push this session's 2 local commits**, then **deploy** (the
   Upgrade block in [70_deployment.md](70_deployment.md); watch the
   four startup log lines). Order matters: scrub rewrites history, so
   push the cleanup branch only as part of / after the scrub, not
   before.
- Still open, independent: **YouTube-si= delete** - owner reported
  "reposts but doesn't delete the original"; most likely the bot lacks
  the **Delete Messages** admin right in that chat (code
  reposts-then-deletes and degrades by design). Verify rights before
  touching code.
- Optional: **more game content** - this session did a quality-first
  expansion (not literal 20x on Q&A pools by design); owner can ask
  for more volume in the safe pools (8ball/roast/praise/hangman).

## Read order

1. `handoff.md` (this).
2. [40_moderation.md](40_moderation.md) - the campaign spec.
3. [70_deployment.md](70_deployment.md) - Upgrade + Rollback.
4. [10_scope.md](10_scope.md) - scope, the public-campaign privacy
   exception, env.
5. [devlog/06_cleanup_campaign_rework.md](devlog/06_cleanup_campaign_rework.md)
   (+ 05 for the evidence-grading rationale).
6. memory: `command-output-no-third-party-ping`,
   `ux_moderation_privacy`, `bydlokod-import-workflow`,
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
8. No user-triggered command may emit `@handle` / `tg://user?id=` /
   `text_mention` for a third party, and every targeted command (games,
   moderation) must validate the target is a member of THIS chat. The
   only sanctioned member-notifying output is the owner-approved
   gracekick tag. (`command-output-no-third-party-ping`)
9. Never commit personal data or secrets - no real bios, no exported
   chat data, no personal chat ids, no keys. Test fixtures are
   synthetic. The 31 MB БЫДЛОКОД export is never committed
   (`bydlokod-import-workflow`).
