---
id: handoff
kind: guide
---

# Handoff: next-session action plan

Last rewritten 2026-05-16, end of the privacy-leak + owner-feedback
session. Everything below is post-scrub, post-deploy reality. The
cleanup-campaign baseline (2026-05-15) is unchanged and still current.

## State (what is true right now)

- **History was scrubbed and force-pushed** (owner-authorized).
  `origin/master` + `origin/feat/monthly-stats-dm-import-games-yt` were
  rewritten by `git filter-repo` and force-pushed. A fresh clone of
  origin has **0 residual** PII/infra across all refs; the only
  author/committer identity is the GitHub noreply. **All old SHAs are
  orphaned** - any clone must `git fetch && git reset --hard
  origin/master` (`git pull --ff-only` is impossible post-rewrite).
  Pre-scrub recoverable backup off-repo
  (`~/doxme-preScrub-backup-*.{git,bundle}`; rollback = one
  `git push --force --mirror`). Method + residual-risk in
  [devlog/07_privacy_leak_audit.md](devlog/07_privacy_leak_audit.md).
- **Deployed and healthy.** The host clone was realigned to the
  rewritten `origin/master` and rebuilt; container `bidlobot` =
  `running / health=healthy / restarts=0`, all four startup log lines
  seen, already processing live community updates. The pre-deploy
  container had been `unhealthy` on an old feat-branch build - the
  redeploy cleared it. The fixed code (below) is live.
- `GLM_API_KEY` is **unset on the host** -> `/summarize` is off
  (expected; opt-in). Cleanup campaign wired, command-driven, nothing
  auto-active. No DB migration.
- `go build ./...`, `go test ./...` (all packages ok, 0 failures),
  `go vet`, `gofmt`, `validate.sh` green at the rewritten HEAD.
- Cleanup model: DM `/cleanup <period>` confirm **seeds** a per-chat
  campaign (proven-stale only; never the no-evidence gap) - no kick on
  confirm. The daily scheduler then publicly @-tags a batch, grace
  `CLEANUP_GRACE` (72h), spares anyone who writes/reacts, kicks the
  still-silent. `/cleanup stop` cancels; re-run while active refused.

## Shipped & live this session (verified)

opus-critic-reviewed; a real BLOCKER (a third-party member id still
reachable from master pre-force-push) was caught and resolved, full
re-verify clean.

1. **Stats/games `@`-mention -> inert.** `shared.UserDisplay` /
   `UserDisplayFull` render the handle WITHOUT `@` (never
   `tg://user?id=` / `text_mention`); `resolveQuipTarget` too. Covers
   `/stats*`, monthly nominations, all games, YouTube attribution.
   gracekick's own `mention()` untouched (the sole sanctioned
   notifier). `30_stats.md` corrected.
2. **Import-only name no longer leaks the operator's contacts.** the
   resolver blanks `FirstName` for `KnownVia==SourceImport` -> neutral
   `User <id>`; self-heals on live write. **S1:** `KnownVia`
   precedence made monotonic in `storage.UpsertMember` so a re-import
   (the planned BYDLOKOD backfill) won't downgrade a healed member.
   `35_history_import.md` corrected.
3. **`/duel` validates membership.** `DuelHandler` resolves the
   opponent via `GetMemberByUsername` in THIS chat or refuses (no
   dice, inert rejection - a lurking member is not pinged). The duel
   does not notify the challenged member (inert; owner may revisit).
4. **Game content** (replayability): 8ball 20->~65, roast/praise
   15->40, hangman 44->156 (+ full-pool invariant test), trivia
   26->46, snippets 12->23. Quality/correctness-first, NOT a literal
   20x on Q&A pools - owner can ask for more volume in the safe pools.

## Negatives / not done

- **Telegram behaviour not machine-verified.** Inert render, `/duel`
  rejection, cleanup tag/kick, GLM `/summarize` are logic-tested only -
  Claude cannot drive Telegram. The bot is live; operator should
  eyeball `/stats top` (no pings) and `/duel @stranger` (refused).
- **Creds not rotated.** Owner accepted the
  `TG_BOT_TOKEN`/`GLM_API_KEY` transcript-compromise risk
  (2026-05-16); the scrub never touched them (never in git). Rotation
  stays advisable, owner's deferred call, one-line host `env` change.
- **GitHub-side cache** of pre-scrub commits not purged (needs GitHub
  Support); inherent to any post-facto scrub - treat the leaked data
  as already harvested.
- S4 (accepted tradeoff): one corrupt bbolt membership row keeps a
  campaign non-empty so `/cleanup` re-run is refused; escape is
  `/cleanup stop`.

## Next (open, independent - owner's call)

- **Operator smoke test the live bot** (~5 min): in the chat, `/stats
  top` must list names with **no `@`** and ping nobody; `/duel
  @not_a_member` must be refused; `/duel @member` works.
- **YouTube-si= delete** - owner reported "reposts but doesn't delete
  the original"; most likely the bot lacks the **Delete Messages**
  admin right in that chat (code reposts-then-deletes and degrades by
  design). Verify the right before touching code.
- **BYDLOKOD backfill** still pending (memory `bydlokod-import-
  workflow`): bot not yet in that chat; add-as-admin -> DM `/import`.
- Optional: more game-content volume in the safe pools; rotate creds;
  ask GitHub Support to expire cached pre-scrub commits.

## Read order

1. `handoff.md` (this).
2. [40_moderation.md](40_moderation.md) - the campaign spec.
3. [70_deployment.md](70_deployment.md) - Upgrade + Rollback.
4. [10_scope.md](10_scope.md) - scope, the public-campaign privacy
   exception, env.
5. [devlog/07_privacy_leak_audit.md](devlog/07_privacy_leak_audit.md)
   (the scrub; + 06/05 for the cleanup-campaign rationale).
6. memory: `infra` (deploy host/access - NOT in repo),
   `command-output-no-third-party-ping`, `ux_moderation_privacy`,
   `bydlokod-import-workflow`, `project_direction`.

## Smoke test (run before touching anything)

From the repo root:

```sh
go build ./...                              # silent, exit 0
go test ./... 2>&1 | grep -cE '^ok '        # all packages ok
go test ./... 2>&1 | grep -E 'FAIL|panic' | grep -v 'no test'   # -> none
go vet ./... ; gofmt -l internal/ cmd/      # both -> no output
bash docs/llm/validate.sh                   # -> OK
git fetch origin && git rev-list --left-right --count origin/master...master
#   -> "0	0"  (local == rewritten origin/master). History was
#   force-pushed once; if a clone shows divergence it is on orphaned
#   pre-scrub history -> git reset --hard origin/master.
```

Deploy host (private `infra` memory has path/access; NOT in repo):
realign with `git fetch && git reset --hard origin/master` then
`docker compose up -d --build`; `docker compose logs -f bot` must show,
in order: `starting build=...` -> `authenticated bot=...
can_read_all=<bool>` -> `health server listening addr=:8080` -> `bot
started, polling for updates`. `can_read_all=false` is privacy ON and
expected. `docker inspect bidlobot` health must reach `healthy`.

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
   in the same commit as the code. Devlogs are append-only (a
   speculative same-session entry may be corrected to what actually
   happened - devlog 07 was).
8. No user-triggered command may emit `@handle` / `tg://user?id=` /
   `text_mention` for a third party, and every targeted command (games,
   moderation) must validate the target is a member of THIS chat. The
   only sanctioned member-notifying output is the owner-approved
   gracekick tag. (`command-output-no-third-party-ping`)
9. Never commit personal data or secrets - no real bios, no exported
   chat data, no personal chat ids, no keys. Test fixtures are
   synthetic. The БЫДЛОКОД export is never committed
   (`bydlokod-import-workflow`).
10. Never write infra coordinates (server IP / hostname / host paths /
    ssh lines / topology) into `docs/` or any tracked file. Use
    `<deploy-host>` and keep the real values in the private `infra`
    memory only - same finding bar as a leaked secret.
