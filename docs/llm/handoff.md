# Handoff - 2026-08-16 (xpost v2 + docs v3 session)

## 1. State (what is true right now)

- Working tree (NOT committed, NOT deployed): X-post rework + docs v3
  migration. 31 modified + ~10 untracked files; `xshot/` deleted.
- **X-post v2** (`internal/bot/xpost.go`, rewritten): a status URL in a
  supergroup message becomes ONE bot message - tweet text as caption,
  native photos/videos (sendMediaGroup / single send / text-only),
  canonical link - then the original is deleted. Failures keep the
  original + decline phrase (repost-first contract, same as TikTok).
  Data source: public FixTweet API `api.fxtwitter.com` called directly
  from the bot; videos downloaded from twimg with a host allowlist and
  best-fitting-variant selection (bitrate x duration vs the 50 MiB
  upload cap). Photos pass through as URLs (Telegram fetches), with a
  download+upload fallback retry.
- **SendMediaGroup** added to `internal/shared/tgclient` (rate-limited)
  and the `youtubeMediaSender` interface + `textOnlySender` stub +
  `recYTSender` test fake.
- **xshot sidecar gone**: `xshot/` dir deleted, compose is a single
  `bot` service, no depends_on.
- **docs/llm on v3**: PRD.md (DRAFT - freeze pending owner approval),
  ROADMAP.md (E1-E6 done, E7 xpost v2 active, E8-E11 open), TODO.md
  (this batch, evidence filled), all specs carry v3 frontmatter with
  verified touches, validate.sh replaced with the v3 script.
  10_scope.md was deleted 2026-08-16 - fully absorbed into PRD.md
  (git history preserves it).
- Verified green: `go build ./...`, `go vet` (bot + tgclient), full
  `go test -race ./...` (zero FAIL, includes 10 ProcessXPost pipeline
  tests). `gofmt -l` clean except 4 PRE-EXISTING files not touched by
  this session (gracekick.go, reputation/domain.go + 2 test-adjacent).

## 2. Negatives (what does NOT exist)

- No commit, no push, no deploy of this work (owner gates both).
- No deferred/retry queue for xpost failures (decline only; TikTok
  keeps its queue). ROADMAP E11.
- xpost caption format (`sender / author / text / canonical url`) NOT
  yet owner-approved - approval task in TODO.
- PRD not frozen (DRAFT pending owner approval).
- No browser/renderer anywhere - nothing replaces xshot's card look
  (stats/likes are gone from reposts by design).

## 3. Queue

- TODO.md: 3 open items - PRD freeze approval, caption-format
  approval, VM100 deploy.
- ROADMAP E7 (xpost v2 ship) blocks only on those; E8 (docs v3
  completion) blocks on the freeze; E9-E11 are open carry-overs.

## 4. Read order

1. PRD.md -> ROADMAP.md -> TODO.md
2. 57_xpost.md (xpost v2 contract)
3. 60_architecture.md (middleware order - xpost sits after tiktok)
4. 70_deployment.md (single-service compose)

## 5. Smoke test (run before touching anything)

```sh
go build ./... && go test -race ./...   # expect: zero FAIL lines
cd docs/llm && ./validate.sh            # expect: exit 0
```

After deploy (owner OK): post an X status link in the prod chat ->
expect exactly ONE bot message (caption + media + canonical link) and
the original deleted; logs show `xpost: reposted photos=... videos=...`.

## 6. Agent errors

- First test filter `'TestXPost'` did not match `TestProcessXPost*`
  names (substring), so pipeline tests first ran only in the full
  suite and caught a real bug: the test helper registered the media
  allowlist key as host:port while the code looked up hostname().
  Fixed in the helper; production paths unaffected.
- The fix-unicode extension rewrote the U+2026 ellipsis in
  truncateUTF16 into three ASCII dots, breaking UTF-16 budget math
  (tests caught the +2 overflow). Marker is now explicitly "..." with
  a 3-unit reserve.
