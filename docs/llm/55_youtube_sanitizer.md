---
id: youtube-sanitizer
kind: spec
touches:
  - internal/bot/youtube_sanitizer.go
  - internal/bot/youtube_entities.go
  - internal/bot/youtube_caption.go
  - internal/bot/routes.go
written: 2026-05-15
updated: 2026-10-05
---

# YouTube `si=` sanitizer

See also: [PRD.md](PRD.md), [50_telegram.md](50_telegram.md), [60_architecture.md](60_architecture.md).

NOT the dropped "YouTube Summary" (that was an LLM/GLM dependency, still
dropped - see PRD.md). This is a content-cleanup behavior: strip the
`si=` share-tracking parameter that leaks who-shared-from-where, while
preserving the post byte-for-byte otherwise.

`internal/bot/youtube_sanitizer.go` is a passive supergroup middleware
registered on `sgGroup` AFTER the passive observers (membership/stats/
summarize recorder must see the original human message first) and BEFORE
the TikTok/Instagram reposters and the X-post sidecar. Since 2026-10-05
(PR #3) the flow is **copy-then-delete**: the bot posts one cleaned copy
of the message and deletes the original only after the copy is confirmed.

**Privacy gate**: like all content middlewares it needs BotFather
privacy OFF + bot re-added - a bare YouTube link never reaches the bot
under privacy ON ([50_telegram.md](50_telegram.md)).

## Detection

Scan `msg.Text`+`msg.Entities` and `msg.Caption`+`msg.CaptionEntities`.
A link qualifies only if its host is in {youtube.com, www/m
.youtube.com, youtu.be, youtube-nocookie.com} AND it carries a `si`
query param (key matched after percent-decoding, so `s%69=` counts).
Host-scoped strictly: Spotify and other `si=` links, look-alike hosts
(`youtube.com.evil.com`, userinfo spoofs) are NOT touched.

The cleaning itself (`youtube_entities.go`) works by deleting byte
spans from the ORIGINAL text - no `net/url` re-encoding, so the scheme
casing, param order and percent-encodings of everything else survive
verbatim. `url` entities are authoritative (a bare-scan candidate inside
one is skipped); `text_link` anchors are never scanned as text, but the
hidden `entity.URL` is rewritten. Entity offsets (UTF-16) are remapped
through the deletion spans; entities fully swallowed by a deletion are
dropped. Out-of-range offsets, surrogate-splitting offsets and invalid
UTF-8 fail closed: the message passes through untouched.

## Action (copy-then-delete)

1. Preflight (`preflightReason`): shapes the bot cannot faithfully
   reproduce keep the original as-is, logged, no notice - album items
   (`MediaGroupID`), paid media/paid posts, inline keyboards,
   invoices/payments, media spoilers, automatic channel forwards,
   quotes without a local reply, external reply / reply-to-story /
   checklist / poll-option reply contexts, text over 4096 UTF-16
   units, and content kinds other than text or copyable media.
2. Post the copy FIRST, via the rate-limited sender:
   - text message -> `SendMessage` with the corrected text + entities,
     thread, protection, reply/quote context and the (cleaned) link
     preview options;
   - media with a caption of at most 1024 UTF-16 units ->
     `copyMessage` server-side (no reupload, no file_id round-trip)
     with the cleaned caption + caption entities, thread, protection,
     caption-above-media and reply/quote context preserved;
   - media with a longer caption (Premium 2048 captions reach the bot;
     bots cannot send those back) -> `copyMessages` with
     `RemoveCaption` for the media, then the cleaned caption split
     into text parts of at most 4096 UTF-16 units
     (`youtube_caption.go`), each part replying to the copied media.
     Formatting entities are clipped at part boundaries; a `text_link`
     anchor may be clipped, both halves keep the URL; `url` /
     `mention` / `text_mention` / `custom_emoji` are indivisible and
     move the boundary left. A partial failure rolls back ONLY the
     bot's new messages; the original is never touched.
3. Only after a confirmed copy, `DeleteMessage` the original. A failed
   delete keeps both the copy and the original (visible duplicate,
   lesser evil).

## Exclusions

Skipped: `from == nil`, bot sender, anonymous admin, `sender_chat`
(linked channel) - same predicate family as stats counting - plus
everything preflight rejects above. Albums with a tracked link are
passed through as a whole group.

## Known gaps / decisions

- No attribution header. The v1 header ("X писал(а):") named the
  forwarder rather than the author and forced HTML-escaped reposts
  that dropped all entities; it was removed with the copy rework. The
  copy is a bot message - for manually forwarded channel posts the
  forward header is not reproduced.
- Albums: v1 split the caption item out of the album; now the whole
  album is kept untouched (sanitizing one item cannot be done without
  either splitting the group or reposting it as a new album).
- `edited_message`: a clean link edited later to add `si=` is not
  re-sanitized (the handler only sees new messages).
