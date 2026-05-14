---
id: llm-index
kind: index
---

# docs/llm - LLM-facing reference

Operational reference for BidloBot. English only. Short, structured, cheap to load.
Each entry declares `id` and `kind` in front-matter.

## Entries

- [10_scope.md](10_scope.md) - product scope, dropped features, deployment model, ID scheme
- [20_profiles.md](20_profiles.md) - user profile domain: registration FSM, viewing, updating, copy flow
- [30_stats.md](30_stats.md) - chat statistics: counting rules, buffering, display commands
- [40_moderation.md](40_moderation.md) - warn/mute/ban, admin permissions, 3-strike rule
- [50_telegram.md](50_telegram.md) - Telegram API specifics: chat types, deep linking, anonymous admins, rate limits, error handling, onboarding, shutdown
- [handoff.md](handoff.md) - next-session action plan. Read first.

## Kinds

- `index` - this file
- `spec` - domain rules. Read before changing related code.
- `guide` - reference material

## Update rule

Any change to domain logic updates the matching spec in the same commit.
