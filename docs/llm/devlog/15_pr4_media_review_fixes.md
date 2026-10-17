---
id: devlog-15
kind: log
written: 2026-10-17
---

# PR #4 review fixes: docs sync, slideshow budget, slide classification

PR #4 (`fix(media): YouTube is= links and TikTok photo posts with audio`)
was rebased onto `master` `05028f4` and three review findings were fixed.

## Rebase

- Old tip `2bc4acf`, new tip `ee8a8d1`; `git range-diff` shows all five
  commits as `=` - the patches did not change, only the base.
- The local, uncommitted summarize-persistence work in the PR worktree
  was stashed, restored after the rebase, and verified line-for-line
  (added/removed lines identical). It stays out of the PR.
- The locally running systemd bot was not rebuilt or restarted: the
  installed binary checksum stayed identical.

## Review findings, all confirmed

1. **docs/llm drift.** `56_tiktok_repost.md` still described the photo
   post as a permanent decline; `55_youtube_sanitizer.md` mentioned only
   `si`; `60_architecture.md` had the decline row, no rich-message
   sender, and a payload without `message_thread_id`. All three specs,
   the index entry and this devlog were updated in the fix commit.
2. **One 120s budget for the whole slideshow.** `mirrorDownloadTimeout`
   covered up to 50 sequential fetches plus the soundtrack, so a large
   slideshow could never finish; the failure was transient and `/flush`
   repeated the same doomed batch until the 48h TTL.
   Fix: `slideshowDownloadBudget(attachments)` = 30s + 30s per
   attachment, matching the per-request client timeout.
3. **Oversized slide classified as transient.** A slide over the 10 MiB
   photo cap returned a plain error and was retried forever, while an
   oversized soundtrack was already permanent.
   Fix: `errTikTokImageTooLarge` sentinel; `repostTikTokPhotos` maps it
   to `repostPermanent`, so the flush path sends one decline and drops
   the job.

## Verification

- `go build ./...`, `go vet ./...`, `gofmt -l` clean.
- `internal/bot` photo/comment tests under `-race`: pass, including new
  `TestSlideshowDownloadBudgetScalesWithAttachments` and
  `TestTikTokPhotoPostOversizedSlideIsPermanent`.
- PR CI: `vet + race + build`, `gitleaks`, `docker build` all pass;
  PR is `MERGEABLE` / `CLEAN`.
- Local BIDLOLOCAL binary untouched (no rebuild, no restart); its
  running build predates these commits.
