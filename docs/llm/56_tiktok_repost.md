---
id: tiktok-repost
kind: spec
touches:
  - internal/bot/tiktok_repost.go
  - internal/bot/tiktok_source.go
  - internal/bot/tiktok_photos.go
  - internal/bot/tiktok_audio.go
  - internal/shared/tgclient/slideshow.go
  - internal/bot/deferred.go
  - internal/bot/routes.go
  - internal/storage/deferred_repo.go
  - Dockerfile
written: 2026-08-16
updated: 2026-10-17

# TikTok repost (video and photo slideshows)

Shipped 2026-06 (commits aec900e..81f45c9; wishlist item from 2026-05-26).
Same repost-then-delete shape as the YouTube `si=` sanitizer
([55_youtube_sanitizer.md](55_youtube_sanitizer.md)): when a supergroup
message carries a TikTok link, the bot materializes the payload,
reposts it attributed to the original sender, then deletes the
original. Two payloads exist: a video (`data.play` MP4) and a photo
post (`data.images`), the latter sent as one rich-message slideshow
with the original soundtrack.

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
family as stats counting - plus messages that already carry media
natively (`Video`/`Animation`/`VideoNote`/`Document`, shared
`hasAttachedMedia`): a forwarded channel post that embeds the clip and
credits the source with a caption link is passed through untouched, not
re-downloaded and replaced (measured 2026-10-16: a channel rip with a
hidden `text_link` under the caption emoji was deleted and re-uploaded).
The comment-quote gate (`tiktokCommentDecision`) applies the same
exclusion. Photos stay eligible - a photo beside a permalink asks for
the linked video.

## Video pipeline

1. **Download**, mirror first ([Video source](#video-source)): the tikwm
   API resolves the link and returns a signed, watermark-free MP4 URL
   that the bot streams into `<workdir>/video.mp4`. If the mirror fails,
   `yt-dlp -f b[vcodec=h264]/b --no-playlist -o
   <workdir>/video.%(ext)s` runs up to 3 times with a 2s sleep between
   attempts and a **45s deadline per attempt**. Watermark trim was added
   then **removed** (commit 5d3a12a) - both paths download the file
   as-is. If both fail, the error names each path (`mirror: ...;
   yt-dlp: ...`).
2. **Audio check** via `ffprobe`; a video without an audio stream is
   queued for a later retry (TikTok clips are expected to have audio; a
   muted variant may come back with audio). If ffprobe is missing or
   fails it degrades to "assume audio present", so a broken probe never
   blocks reposts.
3. **Size check**: 50 MiB ceiling (`maxVideoSize`) - Telegram Bot API
   upload cap. The mirror path refuses an oversized video twice: on the
   reported metadata size before downloading, and on the number of bytes
   actually streamed. Oversized -> public decline note, original kept.
4. **Repost**: `SendVideo` with HTML caption `👤 <b>display</b> писал(а):`
   + the original caption. Display name only - no `@`, no
   `tg://user?id=`, no `text_mention` (the
   `command-output-no-third-party-ping` invariant).
5. **Delete** the original ONLY after the repost succeeded. Delete
   failure is logged and the original kept (visible duplicate, lesser
   evil).

Steps 3-5 are the shared tail of `repost_common.go` (`repostVideoTail`),
which the Instagram reposter uses as well
([61_instagram_repost.md](61_instagram_repost.md)); only the detector, the
downloader and the audio gate are TikTok-specific.

## Photo-post pipeline

`internal/bot/tiktok_photos.go` (`repostTikTokPhotos`) materializes
every slide before one atomic rich-message send:

1. **Attachment budget**: `images + soundtrack <= MaxSlideshowPhotos`
   (50, `internal/shared/tgclient/slideshow.go`). Over the limit ->
   permanent decline (`repostPermanent`); nothing is sent and the
   original is kept.
2. **Download**: each `images[i]` goes to `<workdir>/slide-NN.jpg`
   through `downloadCommentImage` (10 MiB per-image cap, the Telegram
   photo limit); the soundtrack goes to `<workdir>/soundtrack.mp3`
   through `downloadTikTokAudio` (50 MiB cap, `maxVideoSize`) and is
   never re-encoded. The batch deadline is
   `slideshowDownloadBudget(attachments)` - 30s base plus 30s per
   attachment, matching `tiktokCommentHTTPClient`'s per-request
   timeout. A fixed 120s budget could never cover 50 sequential
   fetches, so large slideshows used to fail forever through `/flush`;
   the scaled budget removes that class.
3. **Send**: one `SendSlideshow` rich message - all photos plus the
   original audio as a separate player. The audio starts on its own,
   with no autoplay sync to paging. The caption is the same attributed
   HTML as the video path (`👤 <b>display</b> писал(а):` + user text),
   and `message_thread_id` is preserved.
4. **Delete**: the original is deleted only after `SendSlideshow`
   confirms a message ID. A delete failure keeps the original
   (visible duplicate, lesser evil).

Error classes: a slide over the 10 MiB photo cap is **permanent**
(`errTikTokImageTooLarge`) - the URL cannot shrink on a retry, so
`/flush` drops the job after one decline note, exactly like an
oversized soundtrack (`errTikTokAudioTooLarge`). Transport, 429 and
5xx failures stay transient and go to the deferred queue.

## Video source

`internal/bot/tiktok_source.go`. The deployment ISP filters TikTok web
by TLS SNI: the yt-dlp path fetches TikTok pages, and the production
log for 2026-08-26..2026-09-12 holds 24 DNS failures and 7 TLS-EOF
failures from it. tikwm does the page fetch on its own side and serves
the MP4 from a `*.tiktokcdn-us.com` host, which the same egress
reaches: 6/6 fetches of `https://vt.tiktok.com/ZSq5d4Rxh` returned
`200`/`1067861` bytes in 1.1-1.5s (verified 2026-09-12).

Request shape: `GET {tikwmAPIHost}/api/?url=<link>` with a Chrome
`User-Agent`, answer decoded from the `code`/`msg`/`data` envelope
shared with the comment endpoints. `data.play` is the media URL,
`data.size` the reported length, `data.images` the photo-post signal.
Requests go through the shared `tikwmPace` limiter, so the mirror's
free tier (about one request per second) sees one client for comments
and downloads together.

**Photo posts**. A `/photo/` post carries a non-empty `data.images`
array and no usable video stream. Measured 2026-09-12: tikwm answers
such a query with `data.size: 0` and a `data.play` URL on a music host
that never serves an MP4 - for these posts `play` is the soundtrack,
never treated as a video. `downloadTikTok` returns
`tiktokPost{Images, AudioURL, AudioTitle, AudioPerformer}` and the
reposter sends a slideshow ([Photo-post pipeline](#photo-post-pipeline))
instead of a video. A post without images keeps the video path (fall
back to yt-dlp, queue on failure).

## Failure handling

- **Send failure** (`SendVideo`) splits by class
  (`processTikTok`). Permanent is a 4xx from Telegram (bad file, chat
  forbidden, too large): it gets the public decline note and is never
  queued. Everything else is replayable - a transport fault (timeout,
  reset, DNS) never reached Telegram, and a 429 or 5xx outlived the
  retry ladder - so it is persisted to the deferred queue and `/flush`
  replays it. Measured 2026-09-16: three reposts were lost
  because `sendVideo` timed out, the transport retry reused the same
  `*os.File` that telego had already streamed into the multipart part,
  and Telegram answered `400 file must be non-empty`; the media wrappers
  now rewind their bodies before every attempt
  ([60_architecture.md](60_architecture.md) "Failure handling").
- Download failure or missing audio -> the job is persisted to the
  **per-user deferred retry queue** (`deferred_jobs`, type `tiktok`,
  payload `RepostPayload{URL, Username, FirstName, Caption,
  MessageThreadID}`; see [60_architecture.md](60_architecture.md)
  "Deferred queue"). The original message is never deleted on failure.
  The thread id keeps a retried post in its original topic; older rows
  without the field stay readable and fall back to the general topic.
- Slideshow download failure (transport, 429, 5xx, batch deadline) ->
  transient, same deferred queue. Retried slideshows keep the
  attachments cap, so a 50-slide post can succeed on the next flush
  instead of dying on a fixed budget.
- Over-limit slideshow (`images + audio > 50`), an over-cap slide
  (`errTikTokImageTooLarge`, >10 MiB) and an over-cap soundtrack
  (`errTikTokAudioTooLarge`, >50 MiB) -> public decline note, **never
  queued**: no retry can shrink the source. In the flush path
  (`tryTikTokExport`) such a job is dropped after the note.
- Too-large video, stat/open errors, and 4xx send rejections -> public
  decline note (`sendDecline`, randomized phrase from the failure
  catalog), no enqueue. In the flush path (`tryTikTokExport`) such a job
  is dropped after the note, so `/flush` does not re-download it for the
  rest of the TTL.
- Runs **fire-and-forget** through `shared.Go` (`shared.Go(a.log,
  "tiktok", ...)`), which wraps the goroutine in a `recover` so a panic
  cannot take the process down; the per-update ctx is cancelled when the
  handler returns, so the body builds its own `context.Background()`.
  NOT tracked in `App.inFlight` - best-effort, a shutdown may lose one
  in-flight repost (documented tradeoff).

## Exclusions & gaps (v1)

- Skipped: bots, anonymous admins, `sender_chat` forwards, nil sender,
  messages that already carry media (`hasAttachedMedia`).
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

The runtime image installs `yt-dlp` (pinned release 2026.07.04,
sha256-checked) and `ffmpeg`/`ffprobe`
([70_deployment.md](70_deployment.md)). yt-dlp is the fallback path
only; the mirror needs nothing but the shared HTTP client. No env
vars; the middleware is always on.
