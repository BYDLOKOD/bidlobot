---
id: tiktok-repost
kind: spec
touches:
  - internal/bot/tiktok_repost.go
  - internal/bot/deferred.go
  - internal/bot/routes.go
  - internal/storage/deferred_repo.go
  - Dockerfile
written: 2026-08-16
updated: 2026-08-16
---

# TikTok video repost

Shipped 2026-06 (commits aec900e..81f45c9; wishlist item from 2026-05-26).
Same repost-then-delete shape as the YouTube `si=` sanitizer
([55_youtube_sanitizer.md](55_youtube_sanitizer.md)): when a supergroup
message carries a TikTok video link, the bot downloads the video,
reposts it attributed to the original sender, then deletes the original.

`internal/bot/tiktok_repost.go`, a passive supergroup middleware on
`sgGroup` registered AFTER `youtubeSanitizer` and BEFORE `xpostReposter`
(`routes.go:105-112`). All content middlewares run after the passive
observers (membership/stats/summarize recorder) so the original human
message is counted before it is replaced.

## Detection

Scan `msg.Text`+`msg.Entities` and `msg.Caption`+`msg.CaptionEntities`
(`url` and `text_link` entities - a `text_link` URL lives in
`entity.URL`). Host ∈ {tiktok.com, vm.tiktok.com, vt.tiktok.com} with
www./m./vm./vt. prefixes stripped and port dropped; scheme-less URLs
are accepted (`ensureScheme`). Exclusions: `from == nil`, bot sender,
anonymous admin, `sender_chat` (linked channel) - the same predicate
family as stats counting.

## Pipeline

1. **Download** via `yt-dlp -f bv*[ext=mp4]+ba[ext=m4a]/b[ext=mp4]/best
   --no-playlist -o <workdir>/video.%(ext)s`. Up to 3 attempts with 2s
   sleep, 60s per-call timeout. Watermark trim was added then **removed**
   (commit 5d3a12a) - the current pipeline downloads the original file
   as-is.
2. **Size check**: 50 MiB ceiling (`maxVideoSize`) - Telegram Bot API
   upload cap. Oversized -> public decline note, original kept.
3. **Audio check** via `ffprobe`; a video without an audio stream is
   declined (TikTok clips are expected to have audio). If ffprobe is
   missing/fails it degrades to "assume audio present" so a broken
   probe never blocks reposts.
4. **Repost**: `SendVideo` with HTML caption `👤 <b>display</b> писал(а):`
   + the original caption. Display name only - no `@`, no
   `tg://user?id=`, no `text_mention` (the
   `command-output-no-third-party-ping` invariant).
5. **Delete** the original ONLY after the repost succeeded. Delete
   failure is logged and the original kept (visible duplicate, lesser
   evil).

## Failure handling

- Download failure or missing audio -> the job is persisted to the
  **per-user deferred retry queue** (`deferred_jobs`, type `tiktok`,
  payload `TikTokPayload{URL, Username, FirstName, Caption}`; see
  [60_architecture.md](60_architecture.md) "Deferred queue"). The
  original message is never deleted on failure.
- Too-large, stat/open/send errors -> public decline note
  (`sendDecline`, randomized phrase from the failure catalog), no
  enqueue.
- Runs **fire-and-forget** (`go processTikTok(context.Background(),
  ...)`): the per-update ctx is cancelled when the handler returns, and
  a synchronous download/upload would stall the sequential update loop
  for every other member. NOT tracked in `App.inFlight` - best-effort,
  a shutdown may lose one in-flight repost (documented tradeoff).

## Exclusions & gaps (v1)

- Skipped: bots, anonymous admins, `sender_chat` forwards, nil sender.
- `edited_message` not processed (handler only sees new messages).
- Media groups: only the caption-bearing item is handled.
- Reply/forward context lost on the bot repost.
- `text_link` entity URL is used for the download but the inline text
  is not rewritten (UTF-16 offset problem, same as YT sanitizer).

## Privacy

Needs BotFather **privacy OFF** (reads full message text to find the
link) - the same gate as the YT sanitizer and the X post sidecar
([50_telegram.md](50_telegram.md), [PRD.md](PRD.md)).

## Image requirements

The runtime image installs `yt-dlp` (pinned release, sha256-checked)
and `ffmpeg`/`ffprobe` ([70_deployment.md](70_deployment.md)). No env
vars; the middleware is always on.
