# Devlog 09 - 2026-08-16: X-post v2 (single message, no sidecar) + docs v3

## Trigger

Production double-reply on an X link: the bot sent a tweet-card PNG and
then a decline phrase. Root cause chain: the xshot renderer screenshot
succeeded; the video branch always downloaded the top mp4 variant
(1080p/10.4Mbps x 51s ~= 66 MiB) which exceeds the 50 MiB bot upload
cap, so every video tweet ended in a partial failure -> random decline
phrase (failure_catalog.go) as a second message.

## Change

- internal/bot/xpost.go rewritten. New pipeline: FixTweet API
  (api.fxtwitter.com) called directly from the bot (15s timeout, 1 MiB
  cap) -> one message (SendMessage / SendPhoto / SendVideo /
  SendMediaGroup) with caption = sender, user's own words around the
  link, tweet author + text, canonical URL (UTF-16-budgeted truncation;
  user text dropped before tweet text is cut) -> delete original after
  success only. User's own attached photo is re-sent by file_id first.
  Video variants picked offline: highest-bitrate mp4 whose
  bitrate x duration fits 0.9 x 50 MiB; no fitting variant -> video
  skipped, rest still posted (this alone fixes the trigger case).
  Photo URLs are handed to Telegram to fetch; a failed album send
  retries once with bot-downloaded files. Downloads host-allowlisted
  (pbs.twimg.com, video.twimg.com, pic.x.com).
- SendMediaGroup added to tgclient (per-chat rate budget) and the
  youtubeMediaSender surface.
- xshot/ Puppeteer sidecar deleted (dir, compose service, depends_on).
  The renderer only painted a fake tweet card; the API it proxied
  already returns text + direct media URLs. One fxtwitter call replaces
  two, no Chromium boot, no networkidle0 wait - the repost path is now
  seconds, dominated only by video download+upload.
- Docs: 57_xpost.md rewritten; docs/llm migrated to v3 (PRD.md DRAFT,
  ROADMAP.md, TODO.md, v3 frontmatter everywhere, validate.sh v3).
  PRD freeze + caption-format approval + deploy remain owner-gated.

## Verification

- go test ./internal/bot/ -run 'ProcessXPost' -count=1: 10/10 PASS
  (variant selection incl. the 66 MiB regression case, album assembly,
  fallbacks, declines, delete-only-after-send).
- go test -race ./...: zero FAIL across all packages.

## Notes

- The caption marker is three ASCII dots, not U+2026: the fix-unicode
  extension rewrites typography after writes and silently changed the
  marker's UTF-16 length (caught by the budget tests).
- Test allowlist registration must use hostname(), not host, to match
  the production lookup semantics.
