---
id: instagram-repost
kind: spec
touches:
  - internal/bot/instagram_repost.go
  - internal/bot/repost_common.go
  - internal/bot/tiktok_repost.go
  - internal/bot/deferred.go
  - internal/bot/routes.go
  - internal/bot/app.go
  - internal/storage/deferred_repo.go
  - cmd/bidlobot/config.go
  - cmd/bidlobot/main.go
written: 2026-09-25
updated: 2026-09-25
---

# Instagram reel/post repost

A supergroup message carrying an Instagram post permalink gets its video
downloaded, reposted attributed to the sender, and the original deleted -
the same contract as the TikTok and X reposters
([56_tiktok_repost.md](56_tiktok_repost.md)).

The pipeline itself is shared, not copied: `internal/bot/repost_common.go`
holds the sender gate, the yt-dlp retry ladder, the upload-then-delete tail
and the deferred-queue helper. `internal/bot/instagram_repost.go` holds
only what is Instagram-specific (link detection, format selector, egress,
failure table), and `tiktok_repost.go` is the same shape around the tikwm
mirror. Adding a third reposter means a detector plus a downloader, not
another pipeline.

## Detection

`instagramDecision` scans `msg.Text`+`msg.Entities` and
`msg.Caption`+`msg.CaptionEntities`. A `text_link` entity carries its URL in
`entity.URL`; for a plain `url` entity the field is empty (Bot API
populates it for `text_link` only), so bare links are found by
`instagramURLRe` in the text, with sentence punctuation trimmed by
`trailingPunct`.

Host ∈ {instagram.com, instagr.am} after lower-casing, dropping the port
and stripping a `www.` or `m.` prefix (`hostAllowed`, shared with the
TikTok detector). The path must match `^/(reel|reels|tv|p)/<shortcode>`,
so profile, `/stories/...` and `/explore/...` links are ignored. The
`?igsh=` share parameter is kept - yt-dlp ignores it. The link handed to
the downloader is rewritten to `https://www.instagram.com<path>`, because
the extractor's `_VALID_URL` matches only that host: an `m.` or
`instagr.am` link reaches the generic extractor instead, spends the whole
retry ladder and lands in the queue (measured 2026-09-25 on yt-dlp
2026.08.19).

Exclusions (shared `repostableSender`): bots, anonymous admins,
`sender_chat` (linked channel), messages without a sender.

## Pipeline

`instagramReposter` sits on `sgGroup` after `tiktokReposter` in
`routes.go`, so a message carrying links to both services still yields one
repost per service. The download+upload runs through `shared.Go` with
`context.Background()` (the per-update ctx dies with the handler) and
passes through `igSlot` - one Instagram download at a time, the same shape
as `xpostSlot`; a second link arriving while the slot is busy is queued
instead of dropped.

1. **Download** via yt-dlp (see [Video source](#video-source)).
2. **Size check**: 50 MiB ceiling (`maxVideoSize`), the Telegram bot
   upload cap. Oversized -> public decline note, original kept.
3. **Repost**: `SendVideo` with the HTML caption `👤 <b>display</b>
   писал(а):` plus the original caption (`repostCaption`; display name
   only - no `@`, no `tg://user?id=`, no `text_mention`).
4. **Delete** the original only after the repost succeeded; a delete
   failure is logged and the original kept (visible duplicate, lesser
   evil).

**No audio gate, unlike TikTok.** TikTok's gate exists because TikTok
serves a muted variant to non-browser clients; Instagram's progressive
rendition is a muxed H.264+AAC mp4, the video-only DASH renditions are not
selected by the format rule, and a reel may legitimately be silent. A
missing audio track must not queue a job that can never change.

## Video source

yt-dlp only - there is no working mirror. TikTok keeps the tikwm mirror
first because the deployment ISP filters `tiktok.com` by TLS SNI; measured
2026-09-24 no public Instagram resolver works:

| Candidate | Result |
|-----------|--------|
| `kkinstagram.com`, `instagramez.com` | redirect to ad landing pages, no media |
| `ddinstagram.com`, `instafix.app` | do not answer |
| `api.cobalt.tools` | `error.api.auth.jwt.missing` - API key required |
| `picnob.com`, `pixwox.com`, `imginn.com` | Cloudflare 403 for a scripted client |
| Instagram web `/reel/<code>/` and `/embed/captioned/` | JS shell, no server-rendered media |
| Instagram `i.instagram.com/api/v1/...` | HTTP 403 for an anonymous client |

Request shape: `yt-dlp -f 'b[ext=mp4]/b' --no-playlist [--proxy <proxy>]
[--cookies <jar>] -o <workdir>/video.%(ext)s <link>`, up to
`downloadAttempts` (3) attempts with a 2s sleep between them and a 45s
deadline per attempt (`ytDlpRetry`, shared with the TikTok yt-dlp path).

## Failure handling

- **No video at all** - a photo post ("There is no video in this post"),
  an image carousel ("No video formats found!"), or a post Instagram
  describes as an empty media response ("Instagram sent an empty media
  response") - maps to the shared `errNoVideo` sentinel: the retry ladder
  stops after the first attempt, the chat gets the randomized decline
  phrase (`publicPureFailure`), and the job is **never queued**.
- **Login-walled and rate-limited answers stay retryable, by design.** A
  private post or an anonymous rate limit surfaces as a login-required
  sentence ("This content is only available for registered users who
  follow this account", "Requested content is not available, rate-limit
  reached or login required") or as an empty media response, depending on
  the extractor version. The login-required sentences are not permanent
  markers, so such a link runs the whole ladder and lands in the queue.
  That is deliberate: `/flush` retries with the same `--proxy`/`--cookies`,
  so a configured cookie jar can still complete the job, while a decline
  note would drop the link for good.
- **Any other download failure** (DNS, TLS reset, timeout, HTTP 5xx) is
  retryable: the job is persisted to the per-user deferred queue
  (`deferred_jobs`, type `instagram`, payload `RepostPayload{URL,
  Username, FirstName, Caption}`) and `/flush` replays it. The original is
  never deleted on failure.
- **Send failure** splits by class in the shared tail: a 4xx from Telegram
  (bad file, chat forbidden, too large) is permanent -> decline note,
  never queued; everything else (transport fault, 429, 5xx) is queued.
- **Flush path** (`tryInstagramExport`): a permalink that turned out to
  carry no video, or an upload Telegram refuses with a 4xx, is reported
  once and the job is dropped; any other failure keeps it (TTL 48h).

## Egress (deployment-critical)

Instagram is blocked outright on some ISPs (Russia blocks it at the IP
level) and many posts are login-walled even where the host is reachable.
Two optional settings reach the downloader, both validated at startup:

- `INSTAGRAM_PROXY` -> `--proxy`. `socks5h://` resolves DNS through the
  proxy, which is what a filtered egress needs. Scheme allowlist plus a
  required host; empty = direct.
- `INSTAGRAM_COOKIES` -> `--cookies`, a Netscape-format cookie jar for
  posts that need a session. The path must exist and not be a directory;
  empty = anonymous. The path is read **inside the container**, which
  mounts only `./env` and the `bidlobot-data` volume, so the jar has to
  live under `/var/lib/bidlobot/` (place it there once, it survives
  restarts) or the deployment has to add a bind mount.

Both default to unset, so the feature degrades to "every link lands in the
deferred queue" instead of refusing to start.

## Exclusions & gaps (v1)

- `edited_message` not processed (no route feeds it).
- Media groups: only the caption-bearing item is handled.
- Carousels: a multi-item post is declined, not assembled into an album.
- Reply/forward context is lost on the repost.
- `text_link` entity URL is used for the download, but the inline text is
  not rewritten (UTF-16 offset problem, same as the YT sanitizer).
- `instagram.com/share/reel/<token>` share links are not resolved.
- The Instagram author handle is not added to the caption (attribution is
  the Telegram sender, as on the TikTok path).

## Verification

Measured 2026-09-25 against a live public permalink
(`instagram.com/reel/Ddo2_g3MIEN/`) from the development egress, running
this pipeline with the yt-dlp binary swapped per run:

| yt-dlp | result |
|--------|--------|
| 2026.03.17 (the previous pin, sha256-checked against the Dockerfile) | no file: `[Instagram] ... Requested content is not available, rate-limit reached or login required`; the pipeline queued the job and sent nothing |
| 2026.07.04 (the pin now) | 2396150-byte mp4; reposted, original deleted, queue empty |
| 2026.08.19 (the measurement build) | 2396150-byte mp4, same outcome |

Neither `--proxy` nor `--cookies` were needed from that egress; the
deployment egress still decides whether `INSTAGRAM_PROXY` is required.
The pin sits on the last release before the TikTok extractor regression of
2026-08-10 (yt-dlp issue #17403), so the yt-dlp TikTok fallback keeps
working ([56_tiktok_repost.md](56_tiktok_repost.md),
[70_deployment.md](70_deployment.md)).

## Privacy

Needs BotFather **privacy OFF** (reads full message text to find the
link) - the same gate as the YT sanitizer, the TikTok reposter and the X
post reposter ([50_telegram.md](50_telegram.md), [PRD.md](PRD.md)).
