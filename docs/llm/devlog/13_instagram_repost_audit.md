# Devlog 13 - 2026-09-25: Instagram repost audited, yt-dlp pin raised

## Trigger

The owner asked for a review of the uncommitted Instagram diff and of the
implementation itself ("минимум кода, максимум пользы, без нарушений работы
текущих функций"), then to fix what the review found, and finally to test
the feature against a real permalink.

## What the review found and what was fixed

- **The detector accepted hosts the downloader cannot use.** yt-dlp's
  Instagram extractor matches only `https?://(?:www\.)?instagram.com/(p|tv|reels?)/<code>`;
  `instagr.am` and `m.instagram.com` links fell through to the generic
  extractor instead. Measured with yt-dlp 2026.08.19: `instagr.am/p/<code>`
  reached `[generic]` and failed on the network, `m.instagram.com/reel/<code>`
  reached `[generic]` first and delegated only after fetching the page.
  `instagramPostURL` now rewrites the link to
  `https://www.instagram.com<path>[?query]`.
- **A permanent Telegram rejection was re-downloaded on every `/flush`.**
  Both flush paths returned an error for a 4xx, so the job stayed in the
  queue for the whole TTL and re-ran the download each time. A 4xx now
  reports once and drops the job; the rule moved into one helper,
  `isAPIPermanent`, which the shared tail uses as well.
- **The slot branch of the middleware had no test.** The handler body was
  split into `dispatchInstagram`, which a test can call: busy slot queues
  the link, free slot starts the download and releases the slot.
- **`errPhotoPost` was TikTok-named and TikTok-located while Instagram used
  it.** Renamed `errNoVideo`, declared in `repost_common.go`.
- **Documentation corrected:** the cookie path in the examples pointed at
  `/etc/bidlobot/`, which does not exist inside the container (only `./env`
  and the `bidlobot-data` volume are mounted, and `config.Validate` fails
  the start on a missing path); `56_tiktok_repost.md` still named the
  removed `TikTokPayload` and had steps 2 and 3 in the pre-refactor order;
  the devlog 12 claim that `cmd/bidlobot` config tests covered the proxy
  allowlist was false and is now marked as uncovered.

## Live verification and the pin

The owner supplied a public permalink
(`instagram.com/reel/Ddo2_g3MIEN/?stkn=...`). Three sha256-checked yt-dlp
binaries were run through this pipeline:

| yt-dlp | result |
|--------|--------|
| 2026.03.17 (the pin at the time, sha256 matches the Dockerfile) | no file: `[Instagram] ... Requested content is not available, rate-limit reached or login required`; the job was queued, nothing was sent |
| 2026.07.04 | 2396150-byte mp4; reposted, original deleted, queue empty |
| 2026.08.19 (the build the earlier measurements used) | 2396150-byte mp4, same outcome |

The retry ladder and the queue behaved as documented for the failing
binary: three attempts, no decline note, one job of type `instagram`.

The pin moved from 2026.03.17 to **2026.07.04** - the last release before
TikTok changed anti-bot on 2026-08-10 (issue #17403) and the first line
with the current Instagram extraction path. New digest
`6bbb3d314cde4febe36e5fa1d55462e29c974f63444e707871834f6d8cc210ae` (taken
from the release's `SHA2-256SUMS`). All three permanent-failure markers
still exist in 2026.07.04: two in the Instagram extractor, `No video
formats found!` in `YoutubeDL.py`.

## Negatives

- **The deployment egress is unmeasured.** The development machine reached
  Instagram anonymously and needed neither proxy nor cookies; whether the
  production host does is unknown, so `INSTAGRAM_PROXY` may be unnecessary
  there (or necessary).
- **Login-walled and rate-limited answers stay retryable** - a login failure
  sentence is not a permanent marker, so such a link sits in the queue for
  48h. Deliberate: `/flush` retries with the same cookies, and a decline
  would drop the link for good.
- **No mirror fallback**; one resolver, one point of failure.
- **Carousels and photo posts are declined**, not assembled or converted.
- **`tryTikTokExport` has no test**: the tikwm mirror call is not a seam,
  so the flush policy change is covered on the Instagram side only.
- **26 stale specs** reported by `validate.sh` (games, summarize, youtube
  sanitizer, xpost, reputation) - all pre-existing, none in the files this
  session touched.
