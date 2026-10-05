# Devlog 14 - 2026-10-05: YouTube sanitizer reworked to preserve posts (PR #3)

## Trigger

The owner asked for a critical audit of PR #3 ("fix(youtube): preserve
post content when removing share tracking" by gudkovWay), then to make
every change needed to merge it, merge, and answer the author in the
house style.

## What the branch changed (kept as-is after audit)

- The old flow (repost from the bot with a "писал(а):" header,
  HTML-escaped body, media re-sent by file_id, entities dropped,
  reply-fallback note for `text_link`) is replaced by a server-side
  copy: `copyMessage` reproduces media and formatting, the caption or
  text carries the cleaned entities, and the original is deleted only
  after a confirmed copy.
- `internal/bot/youtube_entities.go` (new): `si` removal now deletes
  byte spans from the original text instead of re-encoding through
  `net/url`. Scheme casing, param order and percent-encodings of the
  survivors are preserved byte-for-byte (the old path lower-cased
  `HTTPS` and re-emitted `%20` as `+`). Entity offsets are remapped
  through the deletion spans in UTF-16; `text_link` URLs are rewritten
  in place; out-of-range/surrogate-splitting offsets and invalid UTF-8
  fail closed.
- `internal/bot/youtube_caption.go` (new): captions longer than 1024
  units (Premium 2048 captions reach the bot; bots cannot send them
  back) are split into text parts of at most 4096 units after a
  caption-less `copyMessages` of the media. Indivisible entities
  (`url`, `mention`, `text_mention`, `custom_emoji`) move the boundary
  left; a clipped `text_link` anchor keeps the URL in both halves. A
  partial failure rolls back only the bot's new messages.
- `internal/shared/tgclient/client.go`: `CopyMessage` / `CopyMessages`
  wrappers follow the existing `runWrite` pattern (per-chat budget,
  chat-id rewrite on migration).

## What the audit verified before merging

- `go build`, `go vet`, the full test suite and the full suite under
  `-race` are green on the branch head; master is its ancestor.
- One-off probes (since deleted) confirmed the offset math: astral
  characters before a shortened `url` entity, astral `text_link`
  anchors, fully-deleted and partially-clipped entities, and caption
  splitting all line up byte-for-byte.
- No dead code: `sanitizerSender` / `youtubeMediaSender` /
  `textOnlySender` / `largestPhotoFileID` stay because the TikTok,
  X-post and Instagram reposters use them.

## What was added on top (this session)

- Tests (`test(youtube)` commit): caption splitting (byte retention,
  unit limits, boundary moves, anchor clipping, oversized-entity
  rejection), the long-caption copy flow with its rollback, the media
  short-caption copy, entity-offset locking with astral characters,
  fail-closed error paths, span-editor query edges, the preflight
  table, and the album-kept-whole guarantee. The branch had none of
  these paths covered.
- Docs (`docs(youtube)` commit): `55_youtube_sanitizer.md` rewritten
  for the copy flow, `00_index.md` and `60_architecture.md` lines
  updated, this devlog, `handoff.md`.

## Decisions taken (owner-approved defaults)

- No attribution header on the copy. The v1 header named the forwarder
  rather than the author and forced the entity-dropping HTML repost;
  the copy preserves content 1:1 instead.
- Albums with a tracked link are skipped as a whole group. v1 split
  the caption item out of the album; neither splitting nor re-grouping
  is acceptable, so nothing is touched.

## Negatives

- The copy is a bot message: for manually forwarded channel posts the
  forward header is not reproduced (documented in the spec).
- `edited_message` remains out of scope, as in v1.
- The handler still makes its API calls synchronously in the update
  loop (as v1 did); copyMessage is cheap, so no change was made.
- Deployment is NOT updated: master moved, the production container
  still runs the previous build until the owner deploys.
