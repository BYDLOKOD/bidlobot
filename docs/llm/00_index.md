---
id: llm-index
kind: index
---

# docs/llm - LLM-facing reference

Operational reference for BidloBot. English only. Short, structured, cheap to load.
Each entry declares `id` and `kind` in front-matter.

## Entries

- [10_scope.md](10_scope.md) - current scope, command surfaces (DM-only moderation), dropped/archived features, ID scheme
- [30_stats.md](30_stats.md) - chat statistics: counting rules, buffering, display commands
- [35_history_import.md](35_history_import.md) - Telegram Desktop chat-export bootstrap that seeds membership for cleanup
- [40_moderation.md](40_moderation.md) - DM console: warn/mute/ban/cleanup, admin model, destructive-action safety
- [50_telegram.md](50_telegram.md) - Telegram API specifics: chat types, anonymous admins, rate limits, error handling, onboarding, shutdown
- [60_architecture.md](60_architecture.md) - layered composition (DM console + legacy dispatcher), bbolt schema, invariants, failure matrix
- [70_deployment.md](70_deployment.md) - docker-compose stack, env vars, healthcheck, BotFather setup, backup, rollback
- [handoff.md](handoff.md) - next-session action plan. Read first.

## Devlog

- [devlog/01_dockerization_and_deploy.md](devlog/01_dockerization_and_deploy.md) - 2026-05-14/15: critic-driven hardening, Docker stack, public release, deploy to <deploy-host>.
- [devlog/02_privacy_ux_rework.md](devlog/02_privacy_ux_rework.md) - 2026-05-15: history import + DM-only moderation rework after two opus critic passes.

## Archived (not part of current scope)

- `20_profiles.md` - bio/profile registration domain. Removed from master on 2026-05-14. Code preserved on git tag `v0-bio-archive` and branch `archive/profiles-bio`. The bbolt buckets `profiles` and `profiles_by_chat` are kept as empty placeholders for future revival.

## Kinds

- `index` - this file
- `spec` - domain rules. Read before changing related code.
- `guide` - reference material
- `log` - devlog entry, dry facts about what happened

## Update rule

Any change to domain logic updates the matching spec in the same commit.
