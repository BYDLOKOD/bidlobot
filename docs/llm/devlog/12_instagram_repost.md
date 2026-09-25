# Devlog 12 - 2026-09-25: Instagram reels join the reposters

## Trigger

Reported as "на ссылку из инсты никак не реагирует" - the bot reposts
TikTok videos and X posts but had no route for an Instagram permalink.
Requested behaviour: the TikTok contract (download the reel, repost it
attributed, delete the original).

An external PR (BYDLOKOD/bidlobot#2, fork `kreanx/bidlobot`) implemented
the feature by copying `tiktok_repost.go` - its own retry ladder, its own
pipeline tail, its own enqueue helper, a duplicate header constant and a
duplicate payload struct. That copy was rejected and the work redone on a
shared module.

## Change

- `internal/bot/repost_common.go` (new): the pipeline both reposters use -
  `repostableSender` (bots/anonymous admins/channel senders/nil sender),
  `ensureScheme`, `previewOutput`, `hostAllowed`, `fileFromWorkDir`,
  `repostCaption`, the yt-dlp ladder (`ytDlpRun`/`ytDlpRetry`/
  `ytDlpAttempt` with a per-service argv and failure classifier), the
  upload-then-delete tail (`repostVideoTail` -> `repostSent` /
  `repostPermanent` / `repostTransient`), `enqueueRepostOrFail`,
  `sendDecline`, the `DeferredQueuer` interface and the 50 MiB cap.
- `internal/bot/instagram_repost.go` (new): Instagram-only parts -
  `instagramDecision` (hosts `instagram.com`/`instagr.am`, path
  `^/(reel|reels|tv|p)/<code>`, entity and text scan), `downloadInstagram`
  (`-f b[ext=mp4]/b`, `--proxy`, `--cookies`, permanent-string
  classification), `igSlot` (one download at a time, xpost-shaped),
  `instagramReposter`, `processInstagram`, `tryInstagramExport`.
- `internal/bot/tiktok_repost.go`: now only TikTok-specific code (hosts,
  detector, tikwm mirror, yt-dlp fallback, audio gate, repost index,
  middleware, pipeline). Its tests pass unchanged apart from mechanical
  renames, which is the evidence that the shared tail behaves as before.
- `internal/storage/deferred_repo.go`: `DeferredInstagram` plus one
  `RepostPayload` for both video-repost job types (the JSON keys are
  unchanged, so queued records stay readable).
- `internal/bot/deferred.go`, `routes.go`, `app.go`,
  `cmd/bidlobot/{config,main}.go`, `.env.example`,
  `deploy/env.example`, `README.md`: wiring, `INSTAGRAM_PROXY` /
  `INSTAGRAM_COOKIES` with startup validation, docs.
- Docs: `61_instagram_repost.md` (new spec), architecture file map,
  invariant 4, failure matrix, deferred-queue section, deployment env
  table, index.

Decisions taken from the PR as-is: three permanent yt-dlp strings, the
format rule without an audio gate, and the yt-dlp pin untouched.

## Verification

- `gofmt -l`, `go vet ./...`, `go test ./...` - clean and green (all
  packages, including the untouched TikTok repost suite).
- New tests in `internal/bot/instagram_repost_test.go`: detection table
  (hosts, path kinds, entities, exclusions, trailing punctuation),
  permanent-marker table, the download path against a stub `yt-dlp`
  (attempt counts, argv including `--proxy`/`--cookies`, scheme
  normalisation), and the pipeline (send-then-delete order, owner
  indexing, oversized decline, photo-post decline without queueing,
  queued transient download/send failures, 4xx decline, delete-failure
  tolerance, both flush outcomes).
- `cmd/bidlobot` config validation (`INSTAGRAM_PROXY` scheme allowlist,
  `INSTAGRAM_COOKIES` path) is **not covered by tests**.

## Negatives

- **Not verified against the pinned yt-dlp.** The image ships yt-dlp
  2026.03.17 (pinned for the TikTok extractor regression, issue #17403);
  the Instagram link measurements were taken on 2026.08.19. The
  permanent-marker strings exist in both, but the extraction path differs
  (`window._sharedData` + `instagram.com/graphql/query` versus
  `/api/graphql` with client impersonation), so extraction on the pinned
  binary is an open question. Recorded here rather than fixed.
- **Not deployed.** The deployment egress cannot reach Instagram; the
  feature works there only once `INSTAGRAM_PROXY` is set.
- One resolver means one point of failure; a logged-in cookie jar is the
  only lever for login-walled posts.
- Carousels are declined rather than assembled, and the Instagram author
  handle is not named in the caption.
