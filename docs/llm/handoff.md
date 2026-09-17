# Handoff - 2026-09-17 (upload retry rewind, deployed)

## 1. State (what is true right now)

- **Deployed.** `origin/master` = `2394f1a`; production
  (`veschin@192.168.0.101`, checkout `~/bidlobot`) was reset to it and
  the container recreated: `running healthy` after 8s, log sequence
  `starting` -> `captcha enabled` -> `bot started, polling for updates`.
- **What changed.** `internal/shared/tgclient/client.go`: all five media
  wrappers (`SendPhoto`, `SendVideo`, `SendAnimation`, `SendDocument`,
  `SendMediaGroup`) rewind every file-backed body - media, thumbnail,
  cover, album item - before every retry attempt;
  `internal/bot/tiktok_repost.go`: `processTikTok` queues a transient
  send failure (transport, 429, 5xx) and keeps the decline note for a
  4xx rejection, which no retry can fix.
- **Why.** Four TikTok reposts were lost in 96 hours (17 succeeded).
  Each loss was `sendVideo` failing on the network and the transport
  retry re-uploading the same `*os.File` that telego had already
  streamed to EOF, so Telegram answered `400 file must be non-empty` -
  a class the ladder does not retry. Details and the log correlation are
  in `docs/llm/devlog/11_upload_retry_rewind.md`.
- **Verified.** `go build ./...`, `go vet ./...`, `gofmt -l internal/`
  clean; `go test ./...` green (21 packages); `go test -race` green for
  `internal/shared/...` and `internal/bot/...`; `docs/llm/validate.sh`
  exits 0. The two new tgclient tests fail against the pre-fix helper
  (the retried attempt carried 931 bytes of multipart framing and no
  video, thumbnail or cover payload; the album case 2119 bytes without
  any item payload).
- **Independent review.** A reviewer agent ran the code-guard toolset
  over the diff and closed one gap in the fix: telego also streams a
  file-backed `Cover` (`SendVideo`, `InputMediaVideo`) and
  `InputMediaAudio` album items, so the rewind now covers those too and
  both tests assert the payloads. It also narrowed the permanent branch
  to 4xx: a 429 or 5xx that outlives the ladder is replayable.
- **Summarize model switched** (separate change, no code):
  `~/bidlobot/env` gained `PI_MODEL=deepseek/deepseek-flash`. The
  DeepSeek API exposes exactly `deepseek-flash` and `deepseek-v4-pro`,
  and `deepseek-flash` is the v4.1 flash model. Verified from inside the
  container: `omp -p ... --model deepseek/deepseek-flash` answers.
  `env.bak.20260917-075603` holds the pre-change file.

## 2. Negatives (what does NOT exist)

- **The four lost reposts are not recoverable from the queue.** They
  were never queued - that is the defect. The originals are still in the
  chat, so resending a link reposts it.
- **The two jobs queued on 2026-09-12** (`vt.tiktok.com/ZSqfcymnR`,
  `vt.tiktok.com/ZSq5d4Rxh`) were not re-checked; the owner `/flush` in
  the production chat is still the way to know.
- **Container network flapping persists.** 171 `lookup
  api.telegram.org` / connection timeouts in 96 hours, 120 of them on
  2026-09-16; requests that do succeed take 2.2-6.2s. The fix makes
  uploads survive it, it does not remove it. The 24h recount against the
  79-line baseline of 2026-09-12 is **120**, i.e. worse, not fixed.
- **No backup cron**; the only snapshot is
  `/home/veschin/bidlobot-backups/bidlobot-20260912-082101.db`.
- **`PI_MODEL` lives only in the host env file**, not in `docker-compose.yml`
  or `.env.example`; the local `.env` still runs the in-code default.
- **26 stale specs** reported by `validate.sh` (games, summarize,
  youtube sanitizer, xpost, reputation) - all pre-existing, none in the
  files this session touched.

## 3. Queue

- After the next network blip, confirm the new shape in the logs:
  `tiktok: repost failed transiently, queuing` and no
  `400 "Bad Request: file must be non-empty"` anywhere.
- Owner: `/flush` in the production chat; a failed job must name both
  paths (`mirror: ...; yt-dlp: ...`).
- Owner: send one real `/summarize` to confirm the switched model
  end-to-end in the chat (only an `omp` probe was run from the
  container).
- Investigation left open: why the container's egress to
  `api.telegram.org` times out in bursts while host-side resolution is
  clean (4-7ms). Candidates: Docker DNS upstream rotation, ISP/TSPU
  filtering. Needs a measurement session, not a code change.
- The `monthstats` boundary fix from 2026-09-12 is now deployed as part
  of `2394f1a` (it arrived in `origin` as `32d0220`).

## 4. Read order

1. `docs/llm/devlog/11_upload_retry_rewind.md` - the failure, the
   mechanism, the evidence.
2. `internal/shared/tgclient/client.go` (`rewindUploadBody`,
   `rewindUploadFiles`, `rewindUploadMedia`) - the fix.
3. `docs/llm/56_tiktok_repost.md` "Failure handling",
   `docs/llm/60_architecture.md` "Failure handling" and
   `docs/llm/50_telegram.md` "Rate limits" - the contract.
4. `docs/llm/70_deployment.md` (`PI_MODEL`, `DEEPSEEK_API_KEY`) before
   touching the host env.

## 5. Smoke test (run before touching anything)

```sh
go build ./... && go vet ./... && gofmt -l internal/
go test ./...                # expect 21 ok
go test -race ./internal/shared/... ./internal/bot/...
cd docs/llm && ./validate.sh # expect exit 0
```

Live checks:

```sh
ssh veschin@192.168.0.101 'docker inspect bidlobot --format "{{.State.Status}} {{.State.Health.Status}}"; \
  docker exec bidlobot wget -qO- http://127.0.0.1:8080/health; echo; \
  docker exec bidlobot printenv PI_MODEL DEEPSEEK_API_KEY; \
  docker logs --since 24h bidlobot 2>&1 | grep -c "lookup api.telegram.org"'
```

## 6. Agent errors

- First reading blamed an empty CDN download for `file must be
  non-empty`. The log disproved it: every 400 sat 2-4s after a
  `sendVideo` transport timeout, and the mirror path never failed in 96
  hours. The bug was in the retry, not in the download.
- The first version of the album test failed on `cannot unmarshal object
  into Go value of type []telego.Message` - the fake caller returned a
  single message where `sendMediaGroup` expects an array.
- `rewindUploadBodies(params.Photo, params.Thumbnail)` did not compile:
  `SendPhotoParams` has no `Thumbnail` field in telego v1.8.0.
- **`PI_MODEL` was appended to `~/bidlobot/env` and applied with a bare
  `docker compose up -d` over SSH.** `DEEPSEEK_API_KEY` is a compose
  pass-through from the calling shell, not an env-file entry, so the
  recreated container lost the summarization credential and `omp`
  answered `No API key found for deepseek`. Fixed by piping
  `pass show token/deepseek` into the remote shell and recreating. The
  trap is now in `.omp/skills/bidlobot-deploy/SKILL.md` ("Failure
  modes", "Session mistakes") and in `70_deployment.md`.
