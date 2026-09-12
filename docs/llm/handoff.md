# Handoff - 2026-09-12 (media resilience session)

## 1. State (what is true right now)

- **Deployed.** `origin/master` = `631cb23` ("fix(resilience): contain
  panics, retry transport faults, fetch TikTok via mirror"). Production
  (`veschin@192.168.0.101`, checkout `~/bidlobot`) was recreated from it
  and verified: container `running healthy`, `/health` -> `200
  {"status":"ok"}`, one `"msg":"starting"` entry in the window, zero
  ERROR lines, log sequence `starting` -> `captcha enabled` -> `health
  server listening` -> `bot started, polling for updates`.
- Working tree clean; docs and devlog are in the same commit
  (`devlog/10_media_resilience.md`).
- **P1** `App.recoverMiddleware` is the outermost handler in `Run`
  (panics become `update handler panic recovered` + stack); `shared.Go`
  wraps the six fire-and-forget spawns (flush, tiktok,
  tiktok-comment, tiktok-shortlink, xpost, captcha-welcome).
- **P2** `retry` gained `kindTransport` (3 attempts, 1/2/4s) and
  `Policy.AttemptTimeout`; `tgclient` splits control (20s per attempt)
  from media (240s, 2 transport attempts); telego's fasthttp client has
  65s read / 240s write; the long poll sets `Timeout: 30`.
- **P3** `internal/bot/tiktok_source.go`: download goes mirror-first via
  tikwm (shares the comment pacer), yt-dlp is the fallback with a 45s
  deadline per attempt; both paths are named in one error; photo posts
  (`errPhotoPost`) decline instead of queueing forever.
- **P4 applied.** `docker-compose.yml` pins
  `dns: [192.168.0.1, 1.1.1.1, 8.8.8.8]`; in-container
  `/etc/resolv.conf` now reads `ExtServers: [192.168.0.1 1.1.1.1
  8.8.8.8]`, `Overrides: [nameservers]`, and both
  `api.telegram.org` and `tikwm.com` resolve from inside the container.
  Baseline for the follow-up count (24h before the change): **79**
  lines matching `lookup api.telegram.org`.
- **First backup ever taken** (P5, partially):
  `/home/veschin/bidlobot-backups/bidlobot-20260912-082101.db`,
  2097152 bytes, bbolt page magic `ed0cdaed`. `deploy/backup.sh` cannot
  produce it as a normal user (see section 2).

## 2. Negatives (what does NOT exist)

- **No backup cron.** Installing it needs root on the deploy host and
  `sudo` there requires a password, so it is uninstalled. The entry to
  install and the root hand-run command are in
  `70_deployment.md` "Backup". Until then the only snapshot is the
  hand-made one above.
- **`deploy/backup.sh` cannot run as the deploy user**: `/var/backups`
  is root-owned, `COMPOSE_DIR` defaults to `/opt/bidlobot` (the checkout
  is `/home/veschin/bidlobot`), and the volume path under
  `/var/lib/docker/volumes` is root-only. Verified: as `veschin` it
  stops the bot, prints `ERROR: ... bidlobot.db missing`, and starts it
  again. The root-free alternative (`docker cp`, documented) works.
- **The two queued TikTok jobs are still queued** until the owner runs
  `/flush` in the production chat (`vt.tiktok.com/ZSqfcymnR`,
  `vt.tiktok.com/ZSq5d4Rxh`). Both answer `code:0 success` through the
  mirror from inside the container.
- **`internal/domain/monthstats` boundary race fixed** (commit 2 of this
  session): the flush skipped any chat whose boundary was already
  persisted by the eager first-`Add` write, so `LiveTrackStart` could
  stay pinned at the first-SEEN ts instead of the earliest live message
  ts that `30_stats.md` defines. The gate is gone, the flush now
  persists the running minimum every time (the store no-ops a repeat),
  the test double mirrors the bbolt repository, and
  `TestBufferLiveTrackStartTracksEarliest` covers the ordering
  deterministically. 25/25 green with `-race` (was 8/12 failing).
  **Not deployed yet** - the running container predates this fix.
- Residual risk, accepted: the mirror download reuses the shared 30s
  HTTP client (`tiktokCommentHTTPClient`), so a very large CDN transfer
  can time out and fall through to yt-dlp. Measured throughput (~1 MiB
  in 1.1-1.5s) leaves the bound comfortable for production sizes.

## 3. Queue

- Owner: `/flush` in the production chat; then confirm
  `tiktok flush: reposted` for both jobs. A failure must name both
  paths (`mirror: ...; yt-dlp: ...`).
- Owner: install the root cron entry (needs the host sudo password).
  Decision on 2026-09-12 was **manual backups for now**, so the cron is
  intentionally not installed; re-ask before adding it.
- Deploy the monthstats boundary fix (one recreate, ~15s) when
  convenient.
- Next session, 24h after 2026-09-12 08:20 UTC: recount
  `docker logs --since 24h bidlobot 2>&1 | grep -c 'lookup
  api.telegram.org'` - target 0 against the 79 baseline.

## 4. Read order

1. `docs/llm/56_tiktok_repost.md` (Video source, Pipeline, Failure
   handling) - the new download contract.
2. `docs/llm/60_architecture.md` (App-level middleware, Failure matrix).
3. `docs/llm/50_telegram.md` "Rate limits" + "Error handling" - retry
   classes and panic recovery.
4. `docs/llm/70_deployment.md` "Backup" before touching host backups.

## 5. Smoke test (run before touching anything)

```sh
go build ./... && go vet ./...
go test -race ./...          # expect: only the monthstats flake can fail
cd docs/llm && ./validate.sh # expect: exit 0
```

Live checks:

```sh
ssh veschin@192.168.0.101 'docker inspect bidlobot --format "{{.State.Status}} {{.State.Health.Status}}"; \
  docker exec bidlobot wget -qO- http://127.0.0.1:8080/health; echo; \
  docker exec bidlobot cat /etc/resolv.conf'
```

## 6. Agent errors

- The plan assumed `go test -race ./...` would be fully green; the
  monthstats flake was reproduced on a clean worktree (8/12) to prove it
  predated this change, then traced to three disagreeing layers (store
  min, flush gate, test double) and fixed as its own commit.
- The plan's `withTikwmStub` reuse did not fit the media endpoint (it
  switches on `tikwmPathList`/`tikwmPathReply`), so the new tests carry
  their own `withTikwmMediaServer` helper; the pacer interval is zeroed.
- P3 assumed photo posts are identifiable by an empty `data.play`. A
  live probe showed they carry images *and* a play URL on a music host
  that serves no MP4, so the sentinel is also returned when a post
  carrying images fails to deliver bytes.
- The plan expected `deploy/backup.sh` to succeed when run by hand; it
  cannot as a non-root user. Root cause chain in section 2.
