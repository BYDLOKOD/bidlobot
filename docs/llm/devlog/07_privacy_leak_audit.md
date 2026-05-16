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
  updates carrying the owner's real user id, handle, first name,
  premium flag; `session1.jsonl` also embeds the full bio as a
  message. Used only by the assertion-light
  `internal/bot/replay_test.go`.
- `testdata/chat_export_sample.json` - a sample of the real community
  export (real chat id + many real member ids/names). Orphan:
  referenced only in prose in devlog 04.
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

## Scrub - EXECUTED and force-pushed (owner-authorized 2026-05-16)

The owner explicitly authorized the force-push ("так чтобы в истории
утечек данных не осталось"). The scrub was not just prepared - it was
run against a fresh mirror of `origin`, verified, force-pushed, and the
host redeployed off the rewritten history. Sequence actually performed:

1. Recoverable backup of pre-scrub `origin`: a `--mirror` clone +
   `--all` bundle + ref-sha list, kept off-repo. Rollback is a single
   `git push --force --mirror` from that backup.
2. `git filter-repo` on a fresh `--mirror` clone of `origin`:
   `--invert-paths` removing the always-dead debug `cmd/*`
   (`seed|demo|dbread|smoke|bidlobot-import`) and the two orphan
   testdata files; `--replace-text` redacting every leaked literal
   (owner ids, **all third-party member ids/names from the owner's
   feedback example**, bot id/handle, bio sentence, the LAN server IP)
   to synthetic values; **`--mailmap`** remapping the owner's real
   personal email in *every commit's author+committer* field to the
   GitHub noreply (attribution-preserving, non-leaking).
3. Verification - **content AND metadata** (the load-bearing lesson):
   `git grep` only searches trees, so a grep-only pass is blind to
   author/committer email and commit messages. The first verification
   round was grep-only and the critic caught a third-party id
   (`Vyacheslav`'s) still reachable from `master`; the augmented set
   then verified **0 residual across every ref** for the full needle
   list, single clean identity in all metadata, 0 in messages, and the
   rewritten HEAD `go build`s + full suite green.
4. Force-push of the two origin refs (master + the feat branch) with
   explicit refspecs (mirror push-config disabled). A fresh clone of
   `origin` afterwards: **0 residual** PII/infra across all refs; only
   identity `Oleg Veschin <...noreply.github.com>`.
5. Local dev repo and the deploy host both realigned with
   `git fetch && git reset --hard origin/master` (the rewrite makes
   `git pull --ff-only` impossible); host rebuilt + redeployed, healthy.

Method note for future scrubs: verify CONTENT (`git grep` every needle
across `$(git rev-list --all)` == 0) **and** METADATA
(`git log --all --format='%ae%n%ce%n%an%n%cn' | sort -u`) **and**
messages; enumerate third-party PII, not only the owner's; let the
critic see the pre-push mirror. The transient expr/mailmap files were
operational artifacts, intentionally never committed (they enumerate
the raw literals).

## Post-state / residual risk (not erasure)

Force-push is not erasure. Old SHAs are orphaned but GitHub may still
serve them by direct hash, cached views, and stale PR/fork refs until
GC - **treat the leaked data as already harvested**. For full removal
the owner should ask GitHub Support to expire the cache. Credentials
(`TG_BOT_TOKEN`/`GLM_API_KEY`) were never in git; the owner accepted
their (transcript-based) compromise risk - rotation stays advisable but
is the owner's deferred call, independent of this scrub. Pre-scrub
backup retained for rollback. Deploy host path/access live in private
memory (`infra`), never in this repo.
