---
id: llm-index
kind: index
---

# docs/llm - LLM-facing reference

Operational reference for BidloBot. English only. Short, structured, cheap to load.
Each entry declares `id` and `kind` in front-matter.

## Entries

- [10_scope.md](10_scope.md) - current scope, dropped/archived features, deployment model, ID scheme
- [30_stats.md](30_stats.md) - chat statistics: counting rules, buffering, display commands
- [40_moderation.md](40_moderation.md) - warn/mute/ban, admin permissions, 3-strike rule
- [50_telegram.md](50_telegram.md) - Telegram API specifics: chat types, anonymous admins, rate limits, error handling, onboarding, shutdown
- [handoff.md](handoff.md) - next-session action plan. Read first.

## Archived (not part of current scope)

- `20_profiles.md` - bio/profile registration domain. Removed from master on 2026-05-14. Code preserved on git tag `v0-bio-archive` and branch `archive/profiles-bio`. The bbolt buckets `profiles` and `profiles_by_chat` are kept as empty placeholders for future revival.

## Kinds

- `index` - this file
- `spec` - domain rules. Read before changing related code.
- `guide` - reference material

## Update rule

Any change to domain logic updates the matching spec in the same commit.
