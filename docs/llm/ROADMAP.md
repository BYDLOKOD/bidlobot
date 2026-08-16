# Roadmap

Epics in priority order. E-numbers never reused. New epics append at the end.

## E1: Dockerization and prod deploy - done
Scope: 2026-05-14/15 - critic-driven hardening, Docker image, public release, first prod deploy.
- Hardening pass from independent critic rounds
- Docker image (yt-dlp, ffmpeg) + compose stack, healthchecks, deploy/backup scripts
- Public release and deploy to prod host

## E2: Privacy/UX rework - done
Scope: 2026-05-15 - admin surface moved to DM, history import.
- Moderation reworked to DM-only (inline posts publicly)
- Telegram Desktop export import (/import; later removed with the DM console, caa8f55)

## E3: Load and correctness audit - done
Scope: 2026-05-15 - tooled audit and hot-path fixes.
- Audit with go test -race -cover, golangci-lint, deadcode
- Fixed rate-limiter bypass on the public path (games, /stats)
- Cooldown and zombie-cleanup fixes

## E4: Monthly stats and games era - done
Scope: 2026-05-15 - monthly nominations, 7 mini-games, YouTube si= sanitizer.
- internal/domain/monthstats per-month report engine
- 7 mini-games with rate limits and bounded notices
- YouTube si= strip middleware (repost-then-delete)

## E5: Cleanup campaign rework - done
Scope: 2026-05-15 - /cleanup became a command-started daily campaign; removed with the DM console (caa8f55).
- Evidence grading, live name resolution, window warning
- Campaign engine: daily tag/grace/kick until exhausted (gracekick scheduler stays wired, idle)

## E6: Content-tools era - done
Scope: 2026-05-26..08-11 - captcha+admission, TikTok repost, X-post v1 sidecar, referral, reputation, summarize on OMP/Pi, deferred queue.
- Captcha and owner-gated admission
- TikTok repost (yt-dlp, repost-then-delete, 50 MiB cap)
- X-post v1 sidecar (xshot Puppeteer) - superseded by E7
- Referral catalog and reputation economy
- Summarize migrated GLM to OMP/Pi (DeepSeek V4 Flash, weighted digests with cost)
- Per-user deferred retry queue (/flush)

## E7: Ship X-post v2 single-message repost - active
Scope: 2026-08-16 - drop the xshot sidecar; repost X statuses as one message via the public FixTweet API, repost-then-delete.
- Rework internal/bot/xpost.go (FixTweet fetch, single-message album/text repost, repost-then-delete)
- SendMediaGroup in tgclient with test fakes
- Delete xshot sidecar (dir, compose service, depends_on); compose now a single bot service
- Migrate 57_xpost.md to v3
- Owner approval of the user-facing caption format
- Full test run (go test -race ./...) - done 2026-08-16, zero FAIL
- Deploy to VM100 (requires explicit owner OK) - done 2026-08-16, healthy after 8s, xshot container+image removed

## E8: Docs v3 migration completion - open
Scope: finish the docs/llm v3 layout for this batch.
- PRD freeze approval (owner)
- validate.sh green at migration HEAD
- Delete 10_scope.md after approval - done 2026-08-16 (absorbed into
  PRD.md; git history preserves it)

- Actualize the validate.sh STALE queue (49 hits across 8 specs: games,
  stats, summarize, telegram, sanitizer, architecture drifted over the
  2026-07/08 commits) - one batch, bump updated: only on real edits

## E9: Live-bot operator smoke test - open
Scope: post-deploy verification in the prod chat (owner's call).
- Verify deploy host matches HEAD (status.sh / docker compose ps)
- /stats top; YT si= link; TikTok link; X link (single-message repost); /refreg + /refs; /praise + /rep; admin /summarize 50; captcha on fresh join if CAPTCHA_ENABLED=1

## E10: MigrateChatID rekey gap decision - open
Scope: group-to-supergroup migration does not rekey all domains.
- Extend MigrateChatID to reputation/captcha/deferred/admission/gracekick/leaderboards, or accept and document

## E11: Optional maintenance - open
Scope: owner-prioritized tidy-ups; none blocks release.
- Deferred queue for xpost failures (mirror TikTok's)
- Purge stale GLM_* from local .env and status.sh
- Rotate credentials (deferred from the 2026-05-16 scrub)
