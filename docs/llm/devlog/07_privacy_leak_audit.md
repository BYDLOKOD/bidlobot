---
id: devlog-07
kind: log
---

# 07 - Privacy / personal-data leak: audit + scrub runbook

2026-05-16. Owner reported personal data in the upstream repo and asked
for a full audit plus a prepared (not executed) history scrub.

## Findings

Verified directly against git history (all refs), not assumed.

### Critical - real PII, committed and pushed to `origin`

Introduced in `9851c0b` ("snapshot Go rewrite"), an ancestor of
`origin/master` - i.e. these were public on the remote.

- `testdata/mock_freeform_input.txt` - owner's real bio (job title,
  salary band, birth year, home city, hardware, GitHub). Orphan: no
  code referenced it (the profiles/bio domain was archived).
- `testdata/session1.jsonl`, `session2.jsonl` - recorded Telegram
  updates carrying the owner's real user id `100000007`, `@veschin`,
  first name, premium flag; `session1.jsonl` also embeds the full bio
  as a message. Used only by the assertion-light
  `internal/bot/replay_test.go`.
- `testdata/chat_export_sample.json` - a sample of the real БЫДЛОКОД
  export (real chat id `1009000003`, real user id). Orphan: referenced
  only in prose in devlog 04.
- Deleted-from-HEAD but live in history: `cmd/seed/main.go` (bio +
  `redacted@example.com` + ids), `cmd/demo/main.go`,
  `cmd/dbread/main.go`, `cmd/smoke/main.go`, `cmd/bidlobot-import/*`
  (test chat id).

### Not a git problem - credentials

`.env` (gitignored, **never** in git history - verified: full-token
and GLM-key `git log -S` return empty) holds a live `TG_BOT_TOKEN` and
`GLM_API_KEY`. `.env`'s own comment states the token was already shared
in a chat transcript. **Scrubbing git does nothing for this.** The
owner must rotate both regardless: bot token via @BotFather, GLM key in
the z.ai console.

### Low / accepted

`.claude/settings.json` + hooks contain `/home/veschin/...` paths. The
username `veschin` is intrinsically public (the module path is
`github.com/veschin/bidlobot`), so FS-path exposure adds nothing; not
scrubbed (disproportionate history churn for zero marginal exposure).

### Premise correction

Owner believed the bio test-data folder was already deleted. It was
not: `testdata/mock_freeform_input.txt` was still tracked at HEAD and
present in the working tree until this session.

## Done this session (working tree, reversible, committed locally)

`513b8d5` - orphans deleted; `session{1,2}.jsonl` sanitized JSON-aware
(synthetic ids/names, schema preserved); `replay_test` + full suite
green (21 ok). **Not pushed.**

## Scrub runbook - DRY-RUN on a mirror, NOT executed, NOT force-pushed

Decision: the working-tree fix is autonomous and reversible. The
history rewrite's only value is realized at `git push --force` to
`origin`, which is irreversible on the shared remote; the owner is away
for hours and pairs the return with redeploy. The PII is already
exposed (pushed), so the marginal hours change nothing, while an
incomplete scrub that is force-pushed is worse than a delayed one.
Therefore the scrub was built and dry-run on a throwaway mirror: with
the exact command below, every PII token count across
`git rev-list --all` came back **0** and the rewritten HEAD still
`go build`s and passes all 21 test packages. The force-push is left as
the one coordinated trigger.

**Validation scope (read before trusting the above).** The dry-run
mirrored the **local** repo (`git clone --mirror /home/veschin/ai/doxme`)
at this session's HEAD - i.e. including the `513b8d5` working-tree
cleanup and local-only branches. The runbook below clones from
**`origin`**, which does NOT have `513b8d5` and has its own ref tips.
The command is path/content-based so it is robust to that difference,
but the "0 residual" result was measured against local refs, not
origin's. This dry-run corroborates the command; it does **not**
substitute for the operator's own verification. Step 4 (the residual
grep == 0 on the actual origin mirror) is MANDATORY and gating before
step 6 - do not skip it on the strength of this devlog.

Tool: `git-filter-repo` (official; not installed on the host - install
via `pip install --user git-filter-repo`, or fetch the single-file
script, which is how the dry-run ran:
`curl -fsSL .../newren/git-filter-repo/<ver>/git-filter-repo`).

`scrub-expressions.txt`:

```
100000007==>100000007
200000008==>200000008
1009000002==>1009000002
1009000001==>1009000001
1009000003==>1009000003
redacted@example.com==>redacted@example.com
e2e_test_bot==>e2e_test_bot
TestCity==>TestCity
regex:"username":"alisa00"==>"username":"alisa00"
regex:"first_name":"Алия"==>"first_name":"Алия"
regex:[REDACTED PERSONAL BIO]"]*==>[REDACTED PERSONAL BIO]
```

Procedure (run from a fresh clone; filter-repo refuses a non-fresh
one):

```sh
# 0. Rotate creds FIRST (independent of git): @BotFather, z.ai console.

# 1. Backup - full, recoverable bundle of every ref, plus ref shas.
git -C /home/veschin/ai/doxme bundle create ~/doxme-backup-$(date +%F).bundle --all
git -C /home/veschin/ai/doxme show-ref > ~/doxme-refs-$(date +%F).txt

# 2. Fresh clone to operate on.
git clone --mirror git@github.com:veschin/bidlobot.git /tmp/doxme-scrub.git
cd /tmp/doxme-scrub.git

# 3. Rewrite all history (path removals + content redaction, one pass).
git filter-repo --force \
  --invert-paths \
  --path-glob 'cmd/seed/*' --path-glob 'cmd/demo/*' --path-glob 'cmd/dbread/*' \
  --path-glob 'cmd/smoke/*' --path-glob 'cmd/bidlobot-import/*' \
  --path testdata/mock_freeform_input.txt --path testdata/chat_export_sample.json \
  --replace-text /path/to/scrub-expressions.txt \
  --mailmap /path/to/scrub-mailmap

# scrub-mailmap rewrites BOTH author and committer identity (filter-repo
# applies a mailmap to both). The owner's real personal email was in
# EVERY commit's author+committer field; map it to the GitHub noreply,
# the standard non-leaking, attribution-preserving form. One line:
#   <NN+handle@users.noreply.github.com> <real-personal-email>

# 4. Verify zero residual - CONTENT *and* METADATA. `git grep` only
#    searches trees; author/committer email + commit messages are
#    METADATA and invisible to it. A grep-only pass hid the email leak
#    once already - do not repeat that mistake.
#  4a. content - derive needles from the expressions file (keeps raw
#      PII out of THIS doc); every count must be 0:
awk -F'==>' '{sub(/^regex:/,"",$1);print $1}' /path/to/scrub-expressions.txt \
 | while read -r t; do printf '%s ' "$t"; \
     git grep -lI "$t" $(git rev-list --all) 2>/dev/null | wc -l; done
#  4b. metadata - no real email/name in any identity field:
git log --all --format='%ae%n%ce%n%an%n%cn' | sort -u   # eyeball: no real PII
#  4c. messages - git log --all --format='%B' | grep -i <real-email>  -> none

# 5. Sanity: clone HEAD, go build ./... && go test ./...  (expect 21 ok).

# 6. COORDINATED, IRREVERSIBLE - only after 4a/4b/4c all clean:
git push --force --all   <origin>
git push --force --tags  <origin>
# origin has: master + feat/monthly-stats-dm-import-games-yt. Then
# every existing clone (incl. the deploy host) must re-clone or
# `git fetch && git reset --hard origin/master` - a rewrite breaks
# `git pull --ff-only`.
```

Post-push reality: force-push is not erasure. If the repo was ever
public, treat the PII as already harvested; rotate creds (done in step
0) and, for a public GitHub repo, ask GitHub Support to expire cached
views and stale PR/fork refs.

## Status

Working tree clean and committed (`513b8d5`, local). History scrub
validated, **awaiting the owner's coordinated force-push**. Credential
rotation is the owner's, mandatory, independent of the scrub.
