---
id: llm-index
kind: index
written: 2026-05-14
updated: 2026-08-16
---

# docs/llm - LLM-facing reference (v3)

Operational reference for BidloBot. English only. Short, structured, cheap to load.
Layout: PRD (intent, frozen) -> ROADMAP (epics) -> TODO (current batch) ->
domain specs/guides (code as it IS) -> handoff (session-end snapshot) ->
devlog (immutable history). Run `./validate.sh` after any edit.

## Start here

1. [PRD.md](PRD.md) - product intent, scope in/out, success criteria. FROZEN after approval.
2. [ROADMAP.md](ROADMAP.md) - epics, status, carry-over backlog.
3. [TODO.md](TODO.md) - current work batch, evidence-based acceptance.
4. [handoff.md](handoff.md) - next-session snapshot. Read first after PRD/ROADMAP.

## Entries

- [25_games.md](25_games.md) - mini-games: command/cooldown table, rate-limit + bounded-notice rules, inline (roast/praise moved to reputation)
- [30_stats.md](30_stats.md) - chat statistics: counting rules, buffering, lifetime + monthly nominations, display, MSK day boundary
- [45_summarize.md](45_summarize.md) - admin-only `/summarize`: OMP/Pi CLI + DeepSeek V4 Flash, RAM-only window, weighted digest with cost, deferred retry, privacy
- [50_telegram.md](50_telegram.md) - Telegram API specifics: chat types, anonymous admins, rate limits + per-user cooldown notice, error handling, onboarding + admission gate, captcha, shutdown
- [55_youtube_sanitizer.md](55_youtube_sanitizer.md) - YouTube `si=` strip: host scoping, repost-then-delete, exclusions, v1 gaps, privacy gate
- [56_tiktok_repost.md](56_tiktok_repost.md) - TikTok video repost: yt-dlp download, 50 MiB cap, audio check, repost-then-delete, deferred queue on failure
- [57_xpost.md](57_xpost.md) - X/Twitter post repost: FixTweet API, single-message album (text + photos + videos + canonical link), repost-then-delete, variant size selection, single-slot concurrency
- [58_referral.md](58_referral.md) - referral catalog: /refs /refreg /refreport, chat-scoped buckets, registration UX, moderation
- [59_reputation.md](59_reputation.md) - reputation economy: /praise /roast /rep /reptop, balance rules, live membership check
- [60_architecture.md](60_architecture.md) - layered composition, middleware order, bbolt schema, invariants, failure matrix, deferred queue
- [65_admission.md](65_admission.md) - owner-only installation gate (BOT_OWNER_ID, LeaveChat) + opt-in new-member captcha with welcome animation
- [70_deployment.md](70_deployment.md) - docker-compose stack (single bot service), env vars, image contents (yt-dlp/ffmpeg/omp), healthcheck, backup, rollback

## Devlog

- [devlog/01_dockerization_and_deploy.md](devlog/01_dockerization_and_deploy.md) - 2026-05-14/15: critic-driven hardening, Docker stack, public release, deploy.
- [devlog/02_privacy_ux_rework.md](devlog/02_privacy_ux_rework.md) - 2026-05-15: history import + DM-only moderation rework after two opus critic passes.
- [devlog/03_load_audit_and_privacy_model.md](devlog/03_load_audit_and_privacy_model.md) - 2026-05-15: load/correctness audit, hot-path fixes (rate-limit/cooldown/zombie), cleanup operating model.
- [devlog/04_monthly_stats_games_yt_dm_import.md](devlog/04_monthly_stats_games_yt_dm_import.md) - 2026-05-15: monthly nominations engine, 7 mini-games, YouTube si= sanitizer, in-process DM history import.
- [devlog/05_cleanup_evidence_grading_and_daily_lifecycle.md](devlog/05_cleanup_evidence_grading_and_daily_lifecycle.md) - 2026-05-15: `/cleanup` evidence grading, live name resolution, window warning; first cut of the daily lifecycle.
- [devlog/06_cleanup_campaign_rework.md](devlog/06_cleanup_campaign_rework.md) - 2026-05-15: reworked `/cleanup` into a command-started campaign; immediate-kick executor removed.
- [devlog/07_privacy_leak_audit.md](devlog/07_privacy_leak_audit.md) - 2026-05-16: PII audit, working-tree sanitize, scrub runbook, creds rotation deferred.
- [devlog/08_content_tools_era.md](devlog/08_content_tools_era.md) - 2026-05-26..08-11: captcha + admission gate, TikTok repost, X-post sidecar, referral, reputation, summarize migrated to OMP/Pi, deferred queue, DM console removed.
- [devlog/09_xpost_v2_and_docs_v3.md](devlog/09_xpost_v2_and_docs_v3.md) - 2026-08-16: X-post reworked to single-message FixTweet repost (xshot sidecar deleted), docs/llm migrated to v3.

## Removed surfaces (code deleted; docs deleted with them)

- `35_history_import.md` - DM `/import` of a Telegram Desktop export. Removed with the DM console (caa8f55, 2026-06). `internal/histimport` was later cut entirely (197d98a).
- `40_moderation.md` - DM moderation console (`/warn /mute /ban /cleanup`). Removed (caa8f55). `gracekick` scheduler + `CLEANUP_*` env survive but are idle (no seeding command).
- `20_profiles.md` - bio/profile registration FSM. Archived 2026-05-14 (`archive/profiles-bio`, `v0-bio-archive`); buckets kept empty.
- `10_scope.md` - superseded by [PRD.md](PRD.md) (absorbed on the 2026-08-16 v3 migration).

## Kinds

- `index` - this file
- `spec` - domain rules. Read before changing related code.
- `guide` - reference material
- `log` - devlog entry, dry facts about what happened

## Update rule

Any change to domain logic updates the matching spec in the same commit.
`validate.sh` must exit 0 before commit; its STALE report is the
actualization queue.
