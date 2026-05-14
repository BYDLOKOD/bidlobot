# CLAUDE.md

## Project state

**BidloBot** - Telegram bot for IT communities. Go rewrite from scratch (previous Clojure implementation scrapped after audit).

What exists now:
- `docs/llm/` - 6 spec files + handoff. Critic-reviewed. Source of truth for all domain logic.
- `docs/prd.md` - monolithic PRD (reference only, domain docs in `docs/llm/` are authoritative)

What does not exist (and must not be assumed):
- Any Go code. No packages, no binary, no tests.
- Architecture document. DB and framework choices not finalized.

## Start here

Read `docs/llm/handoff.md` first. It has current state, next steps, and anti-patterns.

## Build & test

Not applicable yet. Will be updated when Go project is bootstrapped.

## Scope guard

Three features only: **profiles**, **statistics**, **moderation**. Specs in `docs/llm/20-40_*.md`.

Dropped and must not return: YouTube summary, inline query DSL, salary field, zen-lang config, i18n switching, bot-managed admin list. Rationale in `docs/llm/10_scope.md`.

## Documentation

All project docs live in `docs/llm/` following the llm-docs skill format. See `docs/llm/00_index.md` for the full list.

Update rules:
- Domain logic change -> update matching spec in same commit
- Session end -> rewrite `docs/llm/handoff.md`
- Significant work -> write devlog in `docs/llm/devlog/`
