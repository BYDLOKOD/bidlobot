# Handoff - 2026-09-25 (Instagram repost audited, shipped, yt-dlp pin raised)

## 1. State (what is true right now)

- **Committed and pushed.** `master` = `436f17e`, two commits:
  `7fadbe1` (Instagram feature + audit fixes), `436f17e` (yt-dlp pin).
- **Deployed and running.** `deploy.sh --skip-push` rebuilt the image on
  `veschin@192.168.0.101` at HEAD `436f17e`; the container came up healthy
  after 8s, `status.sh` reports `healthy`, health endpoint `ok`, no errors
  in the last 5 minutes, and the logs read `starting` -> `captcha enabled`
  -> `bot started, polling for updates`. Container checks: `yt-dlp
  --version` = 2026.07.04, `DEEPSEEK_API_KEY` present (35 characters), the
  new code is inside the running binary (`instagram-defer` and
  `no video stream in this post` both present), and the container
  downloaded the same 2396150-byte reel anonymously - this deployment needs
  no `INSTAGRAM_PROXY` and no cookie jar.
- **What changed.**
  - `internal/bot/repost_common.go` (new): sender gate, yt-dlp retry
    ladder, upload-then-delete tail, queue helper, caption builder, host
    normaliser, 50 MiB cap, `isAPIPermanent`, `errNoVideo`.
  - `internal/bot/instagram_repost.go` (new): detection, `-f b[ext=mp4]/b`,
    `--proxy`/`--cookies`, permanent-failure table, one download slot,
    `dispatchInstagram`, live and `/flush` entry points.
  - `tiktok_repost.go` reduced to TikTok-specific code; `RepostPayload`
    replaced `TikTokPayload` (JSON keys unchanged); `DeferredInstagram`
    added; `INSTAGRAM_PROXY` / `INSTAGRAM_COOKIES` validated at startup.
  - Audit fixes: links are normalised to `https://www.instagram.com`
    before they reach the downloader; a permanent 4xx on the `/flush` path
    reports once and drops the job instead of re-downloading it for the
    rest of the TTL (both reposters); the sentinel is service-neutral.
  - `Dockerfile`: `YT_DLP_VERSION` 2026.03.17 -> **2026.07.04**
    (sha256 `6bbb3d31...` from the release's `SHA2-256SUMS`).
- **Verified.** `gofmt`, `go vet`, `go build`, `go test ./... -count=1`
  all green, including the new `-race` checks for the slot dispatch and the
  URL normalisation; `docs/llm/validate.sh` 0 errors / 0 warnings.
  Live permalink `instagram.com/reel/Ddo2_g3MIEN/` through this pipeline:
  2026.03.17 queued the job and sent nothing, 2026.07.04 and 2026.08.19
  both fetched the same 2396150-byte file and completed the repost.

## 2. Negatives (what does NOT exist / is not proven)

- **The Telegram upload of a real reel is unmeasured.** The container
  reached Instagram anonymously and downloaded the file, so the download
  half is proven in production; posting a link into a chat and watching the
  repost plus the deletion is still an owner step.
- **Login-walled and rate-limited answers stay retryable** (a login failure
  sentence is not a permanent marker), so such a link sits in the queue for
  48h. Deliberate: `/flush` retries with the same cookies.
- **No mirror fallback**; carousels and photo posts are declined rather than
  assembled.
- **`tryTikTokExport` has no test** - the tikwm mirror call is not a seam.
- **No cookie jar is mounted**, and the container mounts only `./env` plus
  the `bidlobot-data` volume: `INSTAGRAM_COOKIES` must point under
  `/var/lib/bidlobot/` or the deployment needs a bind mount.
- **29 stale specs** by `validate.sh` (was 26). Three are new because
  today's commits touch `routes.go`, `app.go`, `deferred.go` and
  `deferred_repo.go`, which `30_stats.md`, `50_telegram.md` and
  `45_summarize.md` list in `touches`; their content still matches the
  code, so the dates were left alone.

## 3. Queue

- Post-deploy: send one Instagram reel into a production chat and confirm
  the repost plus the deletion. The download half is already verified inside
  the container (`yt-dlp --version` = 2026.07.04, 2396150-byte anonymous
  fetch, the new symbols present in the running binary).
- `INSTAGRAM_PROXY` / `INSTAGRAM_COOKIES` stay unset: the anonymous download
  works from the production egress too. Revisit only if Instagram starts
  login-walling this deployment.
- Give `tryTikTokExport` a seam (`downloadTikTok` as a var) so the flush
  policy is covered for both reposters, or drop the duplicate Instagram
  coverage instead.
- Roll the stale specs forward, or record why not.

## 4. Read order

1. `docs/llm/61_instagram_repost.md` - detection, pipeline, failure classes,
   egress, live verification table.
2. `docs/llm/devlog/13_instagram_repost_audit.md` - what the audit found,
   what was fixed, how the pin was chosen.
3. `internal/bot/repost_common.go` - the shared pipeline.
4. `internal/bot/instagram_repost.go` - Instagram-only parts.
5. `docs/llm/60_architecture.md` ("Failure handling", "Deferred queue") and
   `docs/llm/70_deployment.md` (env table, yt-dlp pin) before touching
   wiring.

## 5. Smoke test (run before touching anything)

```sh
go build ./... && go vet ./... && gofmt -l internal/ cmd/
go test ./...                 # expect all ok
bash docs/llm/validate.sh     # expect 0 errors
```

## 6. Agent errors

- **The brief was "review the diff and the implementation".** The first
  answer was that review, but the next turns drifted into documentation
  edits and deployment questions, and the user had to repeat the request
  three times before the implementation fix (the host normalisation) was
  found. Review the implementation first; docs afterwards.
- **An inherited claim was repeated as fact.** Devlog 12 said
  `cmd/bidlobot` config tests covered the proxy allowlist; no such test
  exists. Check a coverage claim before repeating it.
- **A probe failed for the probe's own reason.** The first end-to-end
  sandbox run reported three attempts and no video; the cause was the
  stub script's shell pattern, not the product. Debug the harness before
  reporting a defect.
- **A doc edit was sloppy**: a devlog sentence was first written as
  "so `/flush` does not re-download it to the TTL" and caught only on
  re-reading.
