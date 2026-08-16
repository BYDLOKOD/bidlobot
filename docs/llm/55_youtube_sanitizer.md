---
id: youtube-sanitizer
kind: spec
touches:
  - internal/bot/youtube_sanitizer.go
  - internal/bot/routes.go
written: 2026-05-15
updated: 2026-05-15
---

# YouTube `si=` sanitizer

See also: [PRD.md](PRD.md), [50_telegram.md](50_telegram.md).

NOT the dropped "YouTube Summary" (that was an LLM/GLM dependency, still
dropped - see PRD.md). This is a content-cleanup behavior: strip the
`si=` share-tracking parameter that leaks who-shared-from-where.

`internal/bot/youtube_sanitizer.go`, a passive supergroup middleware
registered on `sgGroup` AFTER the passive observers (membership/stats/
summarize recorder must see the original human message first) and
BEFORE the TikTok reposter and X-post sidecar (`routes.go:105-112`).
The same repost-then-delete shape is reused by the TikTok reposter
([56_tiktok_repost.md](56_tiktok_repost.md)); the X-post sidecar is
add-only ([57_xpost.md](57_xpost.md)).

**Privacy gate**: like all content middlewares it needs BotFather
privacy OFF + bot re-added - a bare YouTube link never reaches the bot
under privacy ON ([50_telegram.md](50_telegram.md)).

## Detection

Scan `msg.Text`+`msg.Entities` and `msg.Caption`+`msg.CaptionEntities`
(including `url` and `text_link` entities - a `text_link` URL is in
`entity.URL`, not the visible text). A link qualifies only if its host
∈ {youtube.com, www/m/music.youtube.com, youtu.be,
youtube-nocookie.com} AND it carries a `si` query param. Host-scoped
strictly: Spotify and other `si=` links, look-alike hosts
(`youtube.com.evil.com`, userinfo spoofs) are NOT touched.

## Action (repost-then-delete)

1. Repost FIRST as the bot: header `👤 <display> писал(а):` + the body
   with **only** `si` stripped from matched links (every other param /
   link / text preserved). Media re-sent by `file_id` (no size limit).
   Send via the rate-limited wrapper.
2. Only if the repost succeeded, `DeleteMessage` the original.
3. If the repost fails -> original is left intact (a stale `si=` link is
   strictly better than destroyed content). If delete fails (no right) ->
   reposted copy stands, original kept (visible duplicate, lesser evil).

A pure `text_link`-only change (visible body unchanged) is NOT
deleted+reposted - instead a reply with the cleaned link(s) is posted
(can't faithfully reproduce hidden-anchor text).

## Exclusions

Skipped: `from == nil`, bot sender, anonymous admin, `sender_chat`
(linked channel) - same predicate family as stats counting.

## Documented gaps (v1)

- `edited_message`: a clean link edited later to add `si=` is not
  sanitized (handler only sees new messages).
- Media groups/albums: only the caption-bearing item is handled;
  siblings left in place.
- Reply/forward context is lost on the bot repost (no re-threading).
- Entities are dropped on repost (offsets shift after stripping `si`);
  body re-sent as HTML-escaped text under the attribution header.
