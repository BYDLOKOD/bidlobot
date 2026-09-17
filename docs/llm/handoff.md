# Handoff - 2026-09-17 (upload retry rewind)

## 1. State (what is true right now)

- **New commit `b8d170b`** on `master`, local only: not pushed to
  `origin`, not deployed. The running container on
  (`veschin@192.168.0.101`, checkout `~/bidlobot`) still executes
  `631cb23` and was started 2026-09-12T08:21:06Z.
- **What changed.** `internal/shared/tgclient/client.go`: all five media
  wrappers (`SendPhoto`, `SendVideo`, `SendAnimation`, `SendDocument`,
  `SendMediaGroup`) rewind their file-backed bodies before every retry
  attempt; `internal/bot/tiktok_repost.go`: `processTikTok` queues a
  send failure that never reached Telegram and keeps the decline note
  for an API rejection.
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
  (retried attempt carried 431 bytes of multipart framing and no video
  payload).

## 2. Negatives (what does NOT exist)

- **Nothing is deployed.** The container that answers `/health` and
  reposts videos predates the fix, so the loss mode is still live on
  every network blip.
- **The four lost reposts are not recoverable from the queue.** They
  were never queued - that is the defect. The originals are still in the
  chat, so resending a link reposts it.
- **The two jobs queued on 2026-09-12** (`vt.tiktok.com/ZSqfcymnR`,
  `vt.tiktok.com/ZSq5d4Rxh`) were not re-checked this session; the owner
  `/flush` in the production chat is still the way to know.
- **Container network flapping persists.** 171 `lookup
  api.telegram.org` / connection timeouts in 96 hours, 120 of them on
  2026-09-16; requests that do succeed take 2.2-6.2s. The fix makes
  uploads survive it, it does not remove it. The 24h recount against the
  79-line baseline of 2026-09-12 is now **120**, i.e. the resolver
  problem is worse, not fixed.
- **Still undeployed from 2026-09-12**: the `monthstats` boundary fix
  (`TestBufferLiveTrackStartTracksEarliest`).
- **No backup cron**; the only snapshot is
  `/home/veschin/bidlobot-backups/bidlobot-20260912-082101.db`.
- **26 stale specs** reported by `validate.sh` (games, summarize,
  youtube sanitizer, xpost, reputation) - all pre-existing, none in the
  files this session touched.

## 3. Queue

- Owner decision: **deploy `b8d170b`** (one recreate, ~15s). It needs a
  push to `origin` first, or a deploy straight from the local checkout.
- After deploy, watch for two log shapes:
  `tiktok: repost failed on the network, queuing` (new, expected on a
  blip) and the absence of `400 "Bad Request: file must be non-empty"`.
- Owner: `/flush` in the production chat; a failed job must name both
  paths (`mirror: ...; yt-dlp: ...`).
- Investigation left open: why the container's egress to
  `api.telegram.org` times out in bursts while host-side resolution is
  clean (4-7ms). Candidates: Docker DNS upstream rotation, ISP/TSPU
  filtering. Needs a measurement session, not a code change.

## 4. Read order

1. `docs/llm/devlog/11_upload_retry_rewind.md` - the failure, the
   mechanism, the evidence.
2. `internal/shared/tgclient/client.go` (`rewindUploadBody`,
   `rewindUploadBodies`, `rewindUploadMedia`) - the fix.
3. `docs/llm/56_tiktok_repost.md` "Failure handling" and
   `docs/llm/60_architecture.md` "Failure handling" - the contract.

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
