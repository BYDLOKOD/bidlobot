# Handoff - 2026-09-25 (Instagram repost on a shared repost module)

## 1. State (what is true right now)

- **Not deployed, not committed.** `master` = `195eb9a`; this session's
  work sits in the working tree of `~/ai/bidlobot`.
- **What changed.** Instagram reel/post repost added, built on a shared
  module instead of a copy of the TikTok reposter:
  `internal/bot/repost_common.go` (new) holds the sender gate, the yt-dlp
  retry ladder, the upload-then-delete tail, the queue helper, the caption
  builder, the host normaliser, `sendDecline` and the 50 MiB cap;
  `internal/bot/instagram_repost.go` (new) holds only Instagram-specific
  parts (link detection, `-f b[ext=mp4]/b`, `--proxy`/`--cookies`,
  permanent-string table, one download slot);
  `internal/bot/tiktok_repost.go` was reduced to TikTok-specific code and
  now calls the same shared parts. `DeferredInstagram` +
  one `RepostPayload` (was `TikTokPayload`) in
  `internal/storage/deferred_repo.go`; dispatch in `deferred.go`;
  middleware in `routes.go` after `tiktokReposter`; egress settings
  `INSTAGRAM_PROXY` / `INSTAGRAM_COOKIES` validated in `config.go` and
  wired in `main.go`.
- **Why.** An external PR (BYDLOKOD/bidlobot#2, fork `kreanx/bidlobot`)
  implemented the feature by duplicating the whole TikTok pipeline. The
  rework keeps the behaviour and drops the duplication.
- **Verified.** `gofmt -l internal/ cmd/` empty, `go vet ./...` clean,
  `go test ./...` green (all packages), `bash docs/llm/validate.sh`
  0 errors / 2 warns (new files have no git history yet). The TikTok
  repost suite passes with mechanical renames only - that is the evidence
  the shared tail did not change behaviour.
- Decisions taken from the PR unchanged: the three permanent yt-dlp
  strings, no audio gate, the yt-dlp pin untouched.

## 2. Negatives (what does NOT exist / is not proven)

- **Instagram extraction is not verified on the pinned yt-dlp.** The
  image ships 2026.03.17 (pinned for the TikTok extractor regression,
  issue #17403); every link measurement was taken on 2026.08.19. The
  permanent strings exist in both, but 2026.03.17 parses
  `window._sharedData` and posts to `instagram.com/graphql/query`, while
  2026.08.19 uses `/api/graphql` with client impersonation. Until this is
  measured, the feature may decline or queue every link in production.
- **The deployment egress cannot reach Instagram**; without
  `INSTAGRAM_PROXY` (or cookies for login-walled posts) every link lands
  in the deferred queue for 48h.
- **No mirror fallback.** One resolver, one point of failure.
- **Carousels and photo posts are declined**, not assembled or converted.
- **26 stale specs** reported by `validate.sh` (games, summarize,
  youtube sanitizer, xpost, reputation, xpost) - pre-existing, none in
  the files this session touched.
- The working tree is uncommitted, so `touches` paths of the new spec
  report "no git history" until the first commit.

## 3. Queue

- Measure Instagram extraction with the pinned binary before trusting the
  feature: run the 2026.03.17 release binary against one public permalink
  from an egress that reaches Instagram, or bump `YT_DLP_VERSION` and
  re-check the TikTok extractor (issue #17403) before shipping.
- Decide the egress settings with the owner: `INSTAGRAM_PROXY` value and
  whether a cookie jar is mounted (`~/bidlobot/env`, then the deploy
  script - a bare `docker compose up -d` drops `DEEPSEEK_API_KEY`).
- After deploy, watch for `instagram: download failed, queuing` and
  `instagram: post has no video, declining`, then `/flush` once.
- Roll the 26 stale specs forward, or record why not.

## 4. Read order

1. `docs/llm/61_instagram_repost.md` - detection, pipeline, failure
   classes, egress, open question about the pinned yt-dlp.
2. `internal/bot/repost_common.go` - the shared pipeline both reposters
   use.
3. `internal/bot/instagram_repost.go` - Instagram-only parts.
4. `docs/llm/60_architecture.md` ("Failure handling", "Deferred queue")
   and `docs/llm/70_deployment.md` (env table) before touching wiring.

## 5. Smoke test (run before touching anything)

```sh
go build ./... && go vet ./... && gofmt -l internal/ cmd/
go test ./...                 # expect all ok
bash docs/llm/validate.sh     # expect 0 errors
```

## 6. Agent errors

- **The TikTok module was edited without being asked.** The instruction
  "reuse components to the maximum" was read as "remove the duplication",
  and removing it rewrites the live reposter. The user objected; the plan
  was confirmed afterwards (shared logic in its own file, service details
  in the service files). Ask before touching a working production path,
  even when the duplication sits there.
- Two questions were asked about decisions the user had already settled
  (which failures are permanent, whether to touch the yt-dlp pin). The
  answer was "the TikTok implementation is the model". Copy the existing
  path's decisions instead of reopening them.
- One edit wrote a placeholder identifier (`tiktokCaptionFree_placeholder`)
  instead of the intended call; caught by the next edit and a build.
- The `deferred.go` edit that added the Instagram branch first deleted the
  `summarize` branch's body; caught by the follow-up edit and a build.
