# CLAUDE.md

## Project state

**BidloBot** - Telegram bot for IT communities. Go, telego + telegohandler
long-poll, embedded bbolt. Content-tools era (since 2026-06): stats,
mini-games, reputation, referral catalog, YouTube/TikTok sanitizers, X-post
sidecar, OMP/Pi summarization, opt-in captcha, owner-gated installation.

What exists now:
- `docs/llm/` - spec files + handoff. Source of truth for all domain logic.
  `docs/llm/00_index.md` lists them; `docs/llm/handoff.md` is the session
  start.
- `docs/prd.md` - historical PRD (reference only; domain docs in
  `docs/llm/` are authoritative; describes the archived bio/statistics/
  moderation v1 scope, much of it removed since).

What does NOT exist (and must not be assumed):
- Any moderation surface (DM console `/warn /mute /ban /cleanup` and
  `/import` were removed 2026-06 in caa8f55).
- The GLM summarization provider (replaced by OMP/Pi + DeepSeek).
- Profile/bio registration FSM (archived).

## Start here

Read `docs/llm/handoff.md` first. It has current state, next steps, and
anti-patterns.

## Build & test

```sh
go build ./...
go test ./...        # 29 packages (23 with tests)
go vet ./...
bash docs/llm/validate.sh   # docs link graph
```

The image additionally needs `yt-dlp`, `ffmpeg`/`ffprobe`, and the
`omp` CLI (pi-coding-agent) for TikTok repost and `/summarize`.

## Scope guard

Current surface: stats, mini-games, reputation, referral catalog,
YouTube `si=` sanitizer, TikTok repost, X-post sidecar, `/summarize`,
captcha + admission gate, `/flush` deferred retries, `/chats` owner
console. Full command table in `docs/llm/10_scope.md`.

Dropped and must not return without an explicit ask: DM moderation
console, `/cleanup` campaign, `/import`, YouTube Summary, inline query
DSL, salary field, zen-lang config, i18n switching, bot-managed admin
list. Rationale in `docs/llm/10_scope.md`.

## Documentation

All project docs live in `docs/llm/` following the llm-docs skill format.
See `docs/llm/00_index.md` for the full list.

Update rules:
- Domain logic change -> update matching spec in same commit
- Session end -> rewrite `docs/llm/handoff.md`
- Significant work -> write devlog in `docs/llm/devlog/`
