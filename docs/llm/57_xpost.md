---
id: xpost
kind: spec
touches: internal/bot/xpost.go
written: 2026-08-16
updated: 2026-08-16
---

# X/Twitter post repost (`xpost`)

Reworked 2026-08 (single-message repost, renderer sidecar retired).
When a supergroup message carries an `x.com`/`twitter.com` status URL,
the bot resolves the tweet through the public FixTweet JSON API
(`api.fxtwitter.com`) and re-sends it as ONE message - tweet text as
caption, every photo/video as native Telegram media, canonical link to
the original - then deletes the user's message. Contract mirrors the
TikTok reposter: repost first, delete only after a successful send; on
failure the original stays and a decline note is posted.

`internal/bot/xpost.go` only. The old `xshot/` Node/Puppeteer sidecar
(painted tweet cards as PNG) was deleted together with its compose
service: the same API it proxied already returns text + direct twimg
media URLs, so the Go bot assembles the message itself.

## Detection

Supergroup message whose text/caption/entities contain a status URL:
host ∈ {x.com, twitter.com} (www./m./mobile. prefixes stripped), path
matching `^/(?:[A-Za-z0-9_]+/status|i/web/status)/[0-9]+/?$`.
Exclusions: bots, anonymous admins, `sender_chat` forwards, nil sender
(the standard predicate family).

## Pipeline (async, single slot)

1. `GET https://api.fxtwitter.com/{user}/status/{id}` (15s timeout,
   1 MiB cap) -> flattened tweet: canonical url, text, author name +
   handle, photo URLs, video variants.
2. Album assembly (`buildXPostItems`):
   - the user's own attached photo (if any) first, by `file_id`;
   - tweet photos as URL references - Telegram fetches them itself;
   - tweet videos downloaded by the bot: `pickXPostVideoVariant`
     chooses the highest-bitrate mp4 whose estimated size
     (bitrate x duration, 10% headroom) fits the 50 MiB upload cap;
     videos with no fitting variant are skipped, not fatal.
   Capped at Telegram's 10 album items.
3. One send (`sendXPostMessage`): `SendMessage` when no media,
   `SendPhoto`/`SendVideo` for a single item, `SendMediaGroup`
   otherwise (caption on the first item). If Telegram refuses to fetch
   a URL photo, one download+upload retry through the bot.
4. On success `DeleteMessage` of the original (delete failure is
   logged, never fatal); on send failure `sendDecline` (randomized
   `publicPureFailure()` phrase) and the original is kept.

Caption layout (plain text, no parse mode; UTF-16 budget against the
1024 caption / 4096 text limits, tweet text truncated with `...`,
user's own text dropped before the tweet text is cut):

    <author:name> (@handle)
    <tweet text>

    <canonical status URL>

Downloads are host-allowlisted (`pbs.twimg.com`, `video.twimg.com`,
`pic.x.com`) so a compromised API response cannot turn the bot into a
proxy.

Concurrency: a single-slot semaphore (`xpostSlot`, cap 1). If the slot
is busy the message is skipped with a decline note (no queue).

## Gaps (v2)

- No deferred/retry queue for xpost failures (only TikTok + summarize
  get one).
- `edited_message` not processed.
- Only the first status URL in a message is expanded.
- Sibling media of the user's own album are not re-sent (same gap as
  the YT sanitizer / TikTok reposter).
- Tweet cards no longer render stats (likes/views) - native media
  albums have no room for them.

## Privacy

Needs BotFather **privacy OFF** (reads message text to find the URL).
Same gate as YT sanitizer + TikTok repost
([50_telegram.md](50_telegram.md)).
