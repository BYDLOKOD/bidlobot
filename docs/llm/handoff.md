---
id: handoff
kind: guide
---

# Handoff: next-session action plan

Last rewritten 2026-05-15, end of the cleanup-campaign-rework session.
Owner feedback + a privacy-leak directive captured 2026-05-16 (see *Known
live bugs* and *Privacy / personal-data leak*); the rest of this file is
the 2026-05-15 cleanup-campaign state and still current.

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

## Known live bugs (owner-reported 2026-05-16, deployed `6942061`)

Captured from owner observation of the deployed bot; fixes in progress
this session (no deploy - owner redeploys later). All three are
user-triggered commands that notify/act on third parties or show a wrong
identity. Common root for 1 & 3: no invariant that user-triggered output
stays inert and that targets are validated chat members. Memory:
`command-output-no-third-party-ping`.

1. **Stats output @-mentions everyone.** `/stats top` / `/stats` /
   monthly nominations render literal `@handle`
   (`1. Олег (@veschin) - 75 ...`). Telegram makes that a real mention,
   so anyone reading stats pings every listed member - reading stats
   mass-summons the chat. **The spec is the root**: `30_stats.md` itself
   mandates `@username` everywhere (`@poweruser`, "Users:
   Name (@username)"). Fix the renderer AND `30_stats.md` in the same
   commit: emit inert text - `@` stripped (`veschin`) or name only;
   never `@handle`, `tg://user?id=`, or `text_mention`.
2. **Import-only users show the operator's contact name.** Members with
   no @username (Telegram-Desktop-imported) render as the *exporting
   account's address-book label*, not their real profile name - e.g.
   `Member (id 409000004)` is Oleg's private contact label. The
   export `from` field is address-book-relative (`35_history_import.md`
   ~line 73 wrongly calls it "Irrelevant"). Fix: resolve import-only
   identity via `getChatMember` at render (the bot is nobody's contact
   -> neutral public name), cache on the membership row, render inertly
   per (1).
3. **`/duel` targets anyone.** `/duel @anyone` and even
   `/duel @1111111111111111111111111` are accepted - challenges users
   not in the chat and malformed/non-existent handles, and pings real
   ones. Fix: validate the target resolves to a present member of THIS
   chat (membership table or `getChatMember` status), reject
   non-members / malformed input with a clear error, render inert. Open
   product Q (owner decides, do not assume): may an in-chat duel
   deliberately notify the challenged member? Pinging non-members is
   never acceptable regardless.

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
secret is already compromised). Audit findings -> a devlog; the ready
runbook -> linked from there and from this handoff on completion.

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
- **Fix the three live bugs + scrub the leak (in progress this
  session).** Recommended: they degrade every `/stats` and `/duel` in
  the live community and the leak is owner-flagged "важно". No deploy -
  owner redeploys on return.
- **Expand mini-game content ~20x.** 8ball and the other phrase-driven
  games need far larger, varied pools for replayability - owner
  explicitly praised the games and asked for the depth (memory:
  `working-style-doxme`). Not a deploy blocker; bundle with the fixes.

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
