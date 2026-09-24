# Devlog 12 - 2026-09-24: Instagram reels join the reposters

## Trigger

Reported as "на ссылку из инсты никак не реагирует" - the bot reposts
TikTok videos and X posts but had no route at all for an Instagram
permalink. Feature requested: same behaviour as the TikTok path
(download the reel, repost it attributed, delete the original).

## What was tried first (and rejected)

The TikTok path leans on the tikwm mirror because the deployment ISP
filters `tiktok.com` by TLS SNI, so the obvious move was to find the
Instagram equivalent. Measured from the deployment egress on 2026-09-24,
none of the candidates works:

- `kkinstagram.com` redirects to an ad landing page (`kkclip.com`),
  `instagramez.com` redirects to an ad network; `ddinstagram.com` and
  `instafix.app` do not answer at all.
- `api.cobalt.tools` answers `error.api.auth.jwt.missing` - the public
  instance now requires an API key.
- `snapsave.app` answers, but only with an obfuscated anti-bot wrapper;
  `picnob.com`, `pixwox.com`, `imginn.com` answer Cloudflare 403 to a
  scripted client.
- Instagram's own web surface is a dead end for an anonymous client:
  `/reel/<code>/` and `/reel/<code>/embed/captioned/` return the **same
  ~640 KB JS shell** for a valid code and a bogus one (no
  server-rendered `og:video`, no `video_url`), and
  `i.instagram.com/api/v1/media/<id>/info/` returns HTTP 403 with a
  Chrome UA and the app `x-ig-app-id`.

`yt-dlp` 2026.08.19 does the job on a normal egress: 4 of 5 sampled
public reels resolved, each as one muxed H.264+AAC mp4 (format 3) plus
video-only VP9 DASH renditions. So the feature ships with yt-dlp as the
only source, and the ISP-blocking gap is closed by two optional env vars
instead of a mirror.

## Root cause of the failures seen on the way

Two yt-dlp behaviours had to be classified before the pipeline could be
correct:

- A photo post, an image carousel, or a login-walled post prints
  `There is no video in this post` / `No video formats found!` /
  `Instagram sent an empty media response`. All three are permanent for
  a given configuration - queueing them would keep a job no retry can
  complete. They now map to the `errIGNoVideo` sentinel: one decline
  note, never queued (the same rule as the TikTok photo-post path).
- Everything else (DNS failure, TLS reset, timeout, HTTP 5xx, rate
  limit) stays retryable and goes to the deferred queue.

The audio gate the TikTok path carries was deliberately dropped here:
that gate exists because TikTok serves muted variants to non-browser
clients, while Instagram's progressive rendition is muxed
(`avc1`+`mp4a` verified in the downloaded artifact) and a reel may
legitimately be silent.

## Change

- `internal/bot/instagram_repost.go` (new): detection
  (`instagram.com`/`instagr.am`, path `^/(reel|reels|tv|p)/<code>`),
  yt-dlp download with 3 attempts x 45s, permanent/transient
  classification, `SendVideo` repost with the inert display-name
  header, delete-after-repost, deferred queuing, and the flush-path
  replay (`tryInstagramExport`).
- `internal/bot/tiktok_repost.go`: the work-dir scan yt-dlp leaves
  behind is now the shared `fileFromWorkDir` helper (no behaviour
  change).
- `internal/storage/deferred_repo.go`: `DeferredInstagram` +
  `InstagramPayload{URL, Username, FirstName, Caption}`.
- `internal/bot/deferred.go`: `/flush` dispatches the new job type.
- `internal/bot/routes.go`: `instagramReposter` on `sgGroup`, after the
  TikTok reposter and before the X reposter.
- `cmd/bidlobot/config.go`, `cmd/bidlobot/main.go`,
  `internal/bot/app.go`: optional `INSTAGRAM_PROXY` /
  `INSTAGRAM_COOKIES` (validated at startup, passed to yt-dlp as
  `--proxy` / `--cookies`), defaulting to a direct anonymous request.
- Docs: `61_instagram_repost.md` (new spec), architecture file map,
  invariant 4, failure matrix, deferred-queue section, deployment env
  table, README feature table, both env templates.

## Verification

- `go vet ./...` clean; `go test ./...` green (29 packages).
- New tests: decision table (hosts, path kinds, entities, exclusions,
  trailing punctuation), caption escaping and the no-ping rule, the
  permanent-marker table, and the download plumbing against a stub
  `yt-dlp` script - attempt counts (1 for a permanent answer, 3 for a
  transport failure), argv shape (`--proxy`, `--cookies`, `--no-playlist`,
  URL normalisation), and the work-dir result. Pipeline tests cover
  send+delete order, owner indexing, too-large decline, photo-post
  decline, queued transient download/send failures, 4xx decline,
  delete-failure tolerance, and both flush-path outcomes.
- Config tests cover the proxy scheme allowlist, the missing cookie-jar
  path and the env names.
- Live evidence (2026-09-24, non-deployment egress): the four reels and
  four `/p/` permalinks quoted in `61_instagram_repost.md` resolved
  through yt-dlp; the downloaded file was an 18,574,971-byte
  `avc1`+`mp4a` mp4.

## Negatives

- **Not deployed.** The branch is a PR; production still ignores
  Instagram links.
- **The deployment egress cannot reach Instagram.** The ISP blocks it,
  so the feature only works there once `INSTAGRAM_PROXY` (or a host
  that is not filtered) is in place; until then every Instagram link
  falls into the deferred queue and `/flush` retries it for 48h.
- **No mirror fallback.** One resolver means one point of failure;
  a logged-in cookie jar (`INSTAGRAM_COOKIES`) is the only lever for
  login-walled posts.
- Carousels are declined rather than assembled into an album, and the
  Instagram author handle is not added to the caption.
