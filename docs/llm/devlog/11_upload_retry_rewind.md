# Devlog 11 - 2026-09-17: retried media uploads uploaded nothing

## Trigger

Reported as "выгрузки тиктоков не работают". Production logs (VM100,
`docker logs bidlobot`, 2026-09-13..2026-09-17, 935 lines) show the
download path is healthy and the upload path is not:

- 17 reposts succeeded, 4 were lost: 2026-09-16 09:32, 10:29, 11:45 and
  2026-09-17 06:32.
- Zero `download failed, queuing` lines: the tikwm mirror did not fail
  once in 96 hours. A live probe from the container on 2026-09-17
  confirmed it (`code:0`, CDN `200 OK`, `Content-Length: 1067861`).
- Every loss was a `sendVideo` failure. Two carried a transport error
  (`fasthttp do request: timeout`, `connection reset by peer`), two
  carried `api: 400 "Bad Request: file must be non-empty"` - and each
  400 arrived 2-4 seconds after a transport error on the same method:

  ```
  [Thu Sep 17 06:32:33] ERROR Execution error sendVideo: request call: fasthttp do request: timeout
  {"time":"2026-09-17T06:32:37","level":"WARN","msg":"tiktok: repost failed; leaving original intact",
   "error":"telego: sendVideo: api: 400 \"Bad Request: file must be non-empty\""}
  ```

- Network background: 171 `lookup api.telegram.org` / connection
  timeouts in 96 hours, 120 of them on 2026-09-16 (the day of three of
  the four losses). Container resolution is fast when healthy (4-7 ms)
  and container requests to `api.telegram.org` take 2.2-6.2 s each.

## Root cause

The transport retry ladder added on 2026-09-12 (commit 631cb23) reuses
one `*os.File` for every attempt while telego streams the body exactly
once:

- `tgclient.SendVideo` passes `func(ctx) { c.bot.SendVideo(ctx, params) }`
  to `retry.Do` (`internal/shared/tgclient/client.go`).
- `params.Video.File` is an `*os.File` opened by `processTikTok`.
- telego builds the multipart body as a pipe stream and copies the
  reader into the file part with `io.Copy(wr, file)`
  (`telegoapi/request_constructor.go:63`, `types.go` `InputFile.File`).

So attempt 1 streams the file to the end of the descriptor and fails on
a network fault; attempt 2 builds a new multipart part from the same
reader, now at EOF, and uploads an empty part. Telegram answers
`400 file must be non-empty`, which `retry.classify` treats as
non-retryable, so the ladder stops and the repost is lost. On top of
that, `processTikTok` queued only download and no-audio failures:
a send failure produced the public decline note and nothing replayable.

## Change

- **Rewind before every attempt** (`internal/shared/tgclient/client.go`).
  `rewindUploadBody` seeks a file-backed body to its start through
  `io.Seeker`; `rewindUploadThumbnail` and `rewindUploadMedia` cover
  thumbnails and album items. Every media wrapper (`SendPhoto`,
  `SendVideo`, `SendAnimation`, `SendDocument`, `SendMediaGroup`) calls
  them as the first statement of its send closure, which `retry.Do`
  runs once per attempt. A reader that is not seekable (a file_id or URL
  reference, both with a nil `File`) is left alone.
- **Queue a network send failure** (`internal/bot/tiktok_repost.go`).
  `processTikTok` now splits on error class: `errors.As(err,
  &telegoapi.Error)` means Telegram answered, so the decline note stands
  and the job is not queued; anything else never reached the API, so the
  job goes to the deferred queue and `/flush` replays it. The
  flush path (`tryTikTokExport`) already keeps a failed job in the
  queue.

## Evidence

- `TestSendVideoRewindsBodyOnTransportRetry` and
  `TestSendMediaGroupRewindsBodiesOnTransportRetry`
  (`internal/shared/tgclient/client_test.go`) drive the real telego
  upload path with a caller that streams the body and fails the first
  attempt with a transport error. Against the pre-fix helper the retried
  attempt uploaded 431 bytes (video) and 1208 bytes (album) with no
  payload - multipart framing around an empty part, the shape Telegram
  rejected; with the fix the payload bytes are present and the send
  succeeds.
- `TestProcessTikTok_TransportSendFailure_Queues` and
  `TestProcessTikTok_APISendFailure_Declines`
  (`internal/bot/tiktok_repost_test.go`) pin the two branches: one
  queued job and no decline message for a transport fault, one decline
  from the failure catalog and no queued job for a 400.
- `go build ./...`, `go vet ./...`, `gofmt -l internal/` clean;
  `go test ./...` green (21 packages), `go test -race` green for
  `internal/shared/...` and `internal/bot/...`.

## Notes

- The rewinding covers `SendAnimation` (welcome GIF) and
  `SendDocument`/`SendMediaGroup` (X-post sidecar) as well: they carry
  the same one-shot reader and had the same latent bug, unobserved so
  far because a retry never happened on those paths.
- The container-side network flapping (DNS and connection timeouts,
  120 in one day) is outside the code: the bot now survives it for
  uploads, but every other Bot API call still fails while it lasts.
  Not addressed here.
- Undeployed at the time of writing; the running container is still
  commit 631cb23.
