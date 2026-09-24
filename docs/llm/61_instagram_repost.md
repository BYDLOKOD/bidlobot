---
id: instagram-repost
kind: spec
touches:
  - internal/bot/instagram_repost.go
  - internal/bot/instagram_repost_test.go
  - internal/bot/tiktok_repost.go
  - internal/bot/deferred.go
  - internal/bot/routes.go
  - internal/bot/app.go
  - internal/storage/deferred_repo.go
  - cmd/bidlobot/config.go
  - cmd/bidlobot/main.go
written: 2026-09-24
updated: 2026-09-24
---

# Instagram reel/post repost

Added 2026-09-24 (branch `feat/instagram-reel-repost`). Same
repost-then-delete shape as the TikTok reposter
([56_tiktok_repost.md](56_tiktok_repost.md)): a supergroup message that
carries an Instagram post permalink gets its video downloaded, reposted
attributed to the original sender, and the original deleted.

`internal/bot/instagram_repost.go`, a passive supergroup middleware on
`sgGroup` registered after `tiktokReposter` and before `xpostReposter`
(`routes.go`). It runs after the passive observers (membership/stats/
summarize recorder) so the original human message is counted before it
is replaced.

## Detection

Scan `msg.Text`+`msg.Entities` and `msg.Caption`+`msg.CaptionEntities`
(`url` and `text_link` entities - a `text_link` URL lives in
`entity.URL`). Host ∈ {instagram.com, instagr.am} with a `www.` or `m.`
prefix stripped and the port dropped; scheme-less URLs are accepted
(`ensureScheme`). The path must match `^/(reel|reels|tv|p)/<shortcode>`,
so profile, `/stories/...` and `/explore/...` links are ignored. The
`?igsh=` share parameter is kept - yt-dlp ignores it. Exclusions: bots,
anonymous admins, `sender_chat` (linked channel), nil sender - the same
predicate family as stats counting.

## Pipeline

1. **Download** via `yt-dlp` (see [Video source](#video-source)).
2. **Size check**: 50 MiB ceiling (`maxVideoSize`, the Telegram bot
   upload cap). Oversized -> public decline note, original kept.
3. **Repost**: `SendVideo` with HTML caption `👤 <b>display</b>
   писал(а):` + the original caption. Display name only - no `@`, no
   `tg://user?id=`, no `text_mention` (the
   `command-output-no-third-party-ping` invariant).
4. **Delete** the original ONLY after the repost succeeded. A delete
   failure is logged and the original kept (visible duplicate, lesser
   evil).

**No audio gate, unlike TikTok.** TikTok's gate exists because TikTok
serves a muted variant to non-browser clients; Instagram's progressive
rendition is a muxed H.264+AAC mp4 (avc1+mp4a verified on the
downloaded artifact 2026-09-24), the video-only VP9 DASH renditions are
never selected, and a reel may legitimately be silent. A missing audio
track must not queue a job that can never change.

## Video source

`yt-dlp` only - there is no working mirror. TikTok keeps the tikwm
mirror first because the deployment ISP filters `tiktok.com` by TLS SNI;
Instagram has no equivalent. Measured 2026-09-24:

| Candidate | Result |
|-----------|--------|
| `kkinstagram.com` | redirects to an ad landing page (`kkclip.com`), no media |
| `instagramez.com` | redirects to an ad network, no media |
| `ddinstagram.com`, `instafix.app` | do not answer |
| `api.cobalt.tools` | `error.api.auth.jwt.missing` - API key required |
| `snapsave.app` | obfuscated anti-bot JS, no usable endpoint |
| `picnob.com`, `pixwox.com`, `imginn.com` | Cloudflare 403 for a scripted client |
| Instagram web `/reel/<code>/` and `/embed/captioned/` | identical ~640 KB JS shell for valid and bogus codes - no server-rendered media |
| Instagram `i.instagram.com/api/v1/media/<id>/info/` | HTTP 403 for an anonymous client |
| `yt-dlp` 2026.08.19 | resolves public permalinks: 4/5 sampled reels, one muxed H.264+AAC mp4 (format 3) plus video-only VP9 DASH renditions |

Request shape: `yt-dlp -f 'b[ext=mp4]/b' --no-playlist
[--proxy <proxy>] [--cookies <jar>] -o <workdir>/video.%(ext)s <link>`,
up to 3 attempts with a 2s sleep between them and a **45s deadline per
attempt** (the per-attempt deadline and the retry count mirror the
TikTok path).

## Failure handling

- **No video at all** - a photo post ("There is no video in this
  post"), an image carousel ("No video formats found!"), or a post
  Instagram will not describe to an anonymous client, i.e. deleted,
  private or login-walled ("Instagram sent an empty media response") -
  is `errIGNoVideo`: the download stops after the first attempt, the
  chat gets the randomized decline phrase (`publicPureFailure`), and the
  job is **never queued** - no retry can turn an image post into a
  video. In the flush path (`tryInstagramExport`) the job is dropped
  after the note.
- **Any other download failure** (DNS, TLS reset, timeout, HTTP 5xx,
  rate limit) is retryable: the job is persisted to the **per-user
  deferred retry queue** (`deferred_jobs`, type `instagram`, payload
  `InstagramPayload{URL, Username, FirstName, Caption}`) and `/flush`
  replays it. The original message is never deleted on failure.
- **Send failure** (`SendVideo`) splits by class: a 4xx from Telegram
  (bad file, chat forbidden, too large) is permanent -> decline note,
  never queued; everything else (transport fault, 429, 5xx) is queued
  for `/flush`.
- Runs **fire-and-forget** through `shared.Go` (`shared.Go(a.log,
  "instagram", ...)`) so a panic cannot take the process down; the
  per-update ctx is cancelled when the handler returns, so the body
  builds its own `context.Background()`. NOT tracked in `App.inFlight` -
  best-effort, a shutdown may lose one in-flight repost (same tradeoff
  as TikTok).

## Egress (deployment-critical)

Instagram is blocked outright on some ISPs (Russia blocks it at the IP
level), and a growing share of posts is login-walled even where the host
is reachable. Two optional env vars therefore reach the downloader:

- `INSTAGRAM_PROXY` -> `--proxy`. `socks5h://` resolves DNS through the
  proxy, which is what a filtered egress needs. Validated at startup
  (scheme allowlist + host); empty = direct.
- `INSTAGRAM_COOKIES` -> `--cookies`, a Netscape-format cookie jar for
  posts that require a session. The path is checked at startup; empty =
  anonymous.

Both default to unset, so the feature degrades to "every link lands in
the deferred queue" rather than failing to start. This is the documented
cost of the mirror-less source.

## Exclusions & gaps (v1)

- Skipped: bots, anonymous admins, `sender_chat` forwards, nil sender.
- `edited_message` not processed (handler only sees new messages).
- Media groups: only the caption-bearing item is handled.
- Carousels: a multi-item post is declined, not assembled into an album.
  Only a post with a single video stream is reposted.
- The Instagram author is not named in the caption (TikTok parity:
  attribution is the Telegram sender only).
- Reply/forward context lost on the bot repost.
- `text_link` entity URL is used for the download but the inline text is
  not rewritten (UTF-16 offset problem, same as the YT sanitizer).
- `instagram.com/share/reel/<token>` share links are not resolved (the
  permalink form is).

## Privacy

Needs BotFather **privacy OFF** (reads full message text to find the
link) - the same gate as the YT sanitizer, the TikTok reposter and the X
post reposter ([50_telegram.md](50_telegram.md), [PRD.md](PRD.md)).

## Image requirements

Nothing new: the runtime image already installs `yt-dlp`
([70_deployment.md](70_deployment.md)). `ffmpeg`/`ffprobe` are NOT used
by this path (no audio gate).
