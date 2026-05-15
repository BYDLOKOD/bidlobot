---
id: llm-index
kind: index
---

# docs/llm - LLM-facing reference

Operational reference for BidloBot. English only. Short, structured, cheap to load.
Each entry declares `id` and `kind` in front-matter.

## Entries

- [10_scope.md](10_scope.md) - current scope, command surfaces (DM-only moderation), dropped/archived features, ID scheme
- [25_games.md](25_games.md) - mini-games: command/cooldown table, rate-limit + bounded-notice rules, inline
- [30_stats.md](30_stats.md) - chat statistics: counting rules, buffering, lifetime + monthly nominations, display
- [35_history_import.md](35_history_import.md) - in-process DM `/import` bootstrap: Telegram Desktop chat-export seeds membership (cleanup) + monthly stats
- [40_moderation.md](40_moderation.md) - DM console: warn/mute/ban, evidence-graded `/cleanup`, the command-started public tag->grace->kick **campaign** (`gracekick`), `/cleanup stop`, destructive-action safety
- [45_summarize.md](45_summarize.md) - admin-only `/summarize`: RAM-only window, GLM provider, error taxonomy, privacy, limitations
- [50_telegram.md](50_telegram.md) - Telegram API specifics: chat types, anonymous admins, rate limits + per-user cooldown notice, error handling, onboarding, shutdown
- [55_youtube_sanitizer.md](55_youtube_sanitizer.md) - YouTube `si=` strip: host scoping, repost-then-delete, exclusions, v1 gaps
- [60_architecture.md](60_architecture.md) - layered composition (DM console + legacy dispatcher), bbolt schema, invariants, failure matrix
- [70_deployment.md](70_deployment.md) - docker-compose stack, env vars, healthcheck, BotFather setup, backup, rollback
- [handoff.md](handoff.md) - next-session action plan. Read first.

## Devlog

- [devlog/01_dockerization_and_deploy.md](devlog/01_dockerization_and_deploy.md) - 2026-05-14/15: critic-driven hardening, Docker stack, public release, deploy to <deploy-host>.
- [devlog/02_privacy_ux_rework.md](devlog/02_privacy_ux_rework.md) - 2026-05-15: history import + DM-only moderation rework after two opus critic passes.
- [devlog/03_load_audit_and_privacy_model.md](devlog/03_load_audit_and_privacy_model.md) - 2026-05-15: load/correctness audit, hot-path fixes (rate-limit/cooldown/zombie), cleanup operating model.
- [devlog/04_monthly_stats_games_yt_dm_import.md](devlog/04_monthly_stats_games_yt_dm_import.md) - 2026-05-15: monthly nominations engine, 7 mini-games, YouTube si= sanitizer, in-process DM history import (import CLI removed).
- [devlog/05_cleanup_evidence_grading_and_daily_lifecycle.md](devlog/05_cleanup_evidence_grading_and_daily_lifecycle.md) - 2026-05-15: `/cleanup` evidence grading (proven-stale vs no-data), live name resolution, window warning; first cut of the daily lifecycle.
- [devlog/06_cleanup_campaign_rework.md](devlog/06_cleanup_campaign_rework.md) - 2026-05-15: reworked `/cleanup` into a command-started campaign (no immediate kick, no env-gated daemon); dead immediate-kick executor removed; two opus-critic rounds.

## Archived (not part of current scope)

- `20_profiles.md` - bio/profile registration domain. Removed from master on 2026-05-14. Code preserved on git tag `v0-bio-archive` and branch `archive/profiles-bio`. The bbolt buckets `profiles` and `profiles_by_chat` are kept as empty placeholders for future revival.

## Kinds

- `index` - this file
- `spec` - domain rules. Read before changing related code.
- `guide` - reference material
- `log` - devlog entry, dry facts about what happened

## Update rule

Any change to domain logic updates the matching spec in the same commit.
