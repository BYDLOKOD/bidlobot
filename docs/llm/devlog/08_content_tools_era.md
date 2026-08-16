---
id: devlog-08
kind: log
---

# Devlog 08: content-tools era (2026-05-26 .. 2026-08-11)

Dense factual log of the post-privacy-leak work on master. HEAD at
close: `47cdd99` (2026-08-11). The cleanup-campaign and DM-console
docs were deleted along with the code; this log records what happened
and why.

## Captcha + welcome animation (2026-05-26 .. 06-03)

- `5359e9a` new-member math captcha with inline buttons; `b8ff8a5`
  wrong answer kicks (rejoinable) instead of retrying; `8394f64`
  bypass rate limiter, mute before message, default 1m timeout.
- `491e510` welcome animation (embedded `welcome.mp4`, `//go:embed`)
  with onboarding questions posted on solve; `ecb8f0f` made the send
  **async** (`go svc.sendWelcome(context.Background(), ...)`) - a
  synchronous 836KB SendAnimation inside the sequential update loop
  stalled every subsequent update ~2s (telego processes updates
  serially; per-update ctx dies with the handler). Lesson captured.
- Owner-gated admission: only `BOT_OWNER_ID` may add the bot; non-owner
  add -> immediate LeaveChat + bounded owner DM notice (2/actor).
- `811ef0a` ponytail audit: -2 deps, -1 file, -5 MB image.

## TikTok repost (2026-06)

- `aec900e` repost with watermark trim -> `5d3a12a` **removed** the trim
  (download original as-is). Watermark-trim was dropped: ffmpeg crop
  risked clipping captions, service-side options added a network/legal
  dependency.
- `d9e8bf8` vt.tiktok.com + scheme-less URLs; `f34e5c2` middleware;
  `b9ccfe4`/`70fabb5` yt-dlp install story: Alpine apk package reverted
  -> pinned upstream release 2026.07.04 with sha256 in the Dockerfile;
  `81f45c9` download retry 3x + **deferred queue** on failure (original
  kept, user retries via `/flush`).
- ffprobe audio check; 50 MiB cap; repost-then-delete.

## Summarize migrated GLM -> OMP/Pi (2026-06..07)

- `caa8f55` "migrate public interactions and summarization": the GLM
  HTTP provider (open.bigmodel.cn / z.ai coding endpoint) replaced by
  the local `omp` CLI (DeepSeek V4 Flash). Same commit removed the DM
  console (moderation + `/cleanup` + `/import`) - the bot became a
  read/game/content tool. `histimport` left unwired.
- `505bfe7`/`3f0af59` forward `DEEPSEEK_API_KEY` through compose (the
  omp CLI reads it from env; the Go binary never sees it); `997707f`
  Debian tini path (runtime alpine -> debian:bookworm-slim for
  ffmpeg/bun).
- `848ad21` weighted digests with cost: relevance-weighted selection
  (threads <5% omitted unless decision/action/fact), footer with
  "расчетная стоимость: $Y" from provider usage.
- `874b198` bound OMP stream memory: per-event 4 MiB scanner cap
  (whole-stream OOM risk).
- `47cdd99` deferred queue extended to summarize (placeholder stays,
  `/flush` retries).

## X-post sidecar (2026-07)

- `3560f78` `xshot` Puppeteer service + Go middleware: tweet card
  screenshot + media repost, add-only (original never deleted),
  single-slot semaphore. Compose service `xshot`, bot
  `depends_on: service_healthy`.

## Referral + reputation (2026-07..08)

- `334bc94` reputation: durable per-chat balance, /praise /roast /rep
  /reptop (roast/praise stopped being curated-template games).
- `04a4bfa` chat-scoped referral catalog + reputation UX refresh
  (/refs /refreg /refreport, paginated picker, duplicate rules,
  admin confirm delete).
- `76ed197` owner chat revocation console (/chats in DM, "Отозвать").
- `82ba6ad` clean stale chats + suppress admission spam (notice bound).

## Deferred queue (2026-08)

- `47cdd99` per-user FIFO retry queue (`deferred_jobs`, 48h TTL, GC
  every 10 min) for failed TikTok exports + summarize; public `/flush`
  (30s cooldown) processes the caller's own jobs.

## State at close (2026-08-11, HEAD 47cdd99)

- 29 Go packages, all `go test` green (incl. race in CI).
- DM moderation console, `/cleanup` campaign seeding, `/import`: gone.
  `gracekick` daily scheduler + `CLEANUP_*` env: still wired, idle.
- Summarization always on; `omp` missing = startup failure.
- Privacy posture: content features need privacy OFF + bot re-add.

## Known gaps (documented, not silently ignored)

- `MigrateChatID` does not rekey reputation/captcha/deferred/admission/
  gracekick/leaderboards.
- xpost has no deferred queue; TikTok/summarize do.
- Local gitignored `.env` still carries stale `GLM_*` keys; `status.sh`
  still greps `GLM_` (cosmetic).
