---
id: handoff
kind: guide
---

# Handoff: next session action plan

Last updated: 2026-04-26, after Go implementation phases 1-6.

## Current state

Branch: master. Go rewrite - core implementation complete.

**What exists:**
- 28 Go files, 3668 LOC
- 3 domains: profiles (FSM + group handlers), stats (buffer + service), moderation (warn/mute/ban)
- Storage: bbolt with 6 buckets + secondary indexes, 9 passing integration tests
- Shared: TelegramAPI interface, AdminCache (60s TTL), target resolution, formatting
- Middleware: logging, stats counting, admin check, predicates (supergroup, private, anon, linked channel)
- Wiring: main.go constructs all deps, routes.go registers full handler tree
- `go build` clean, `go vet` clean, `go test` green

**What does NOT exist:**
- Domain-level tests (only storage integration tests exist)
- `setMyCommands` at startup (command menu not registered)
- Bot onboarding handler (`my_chat_member` - first add to group)
- Rate-limited sender wrapper (currently sends directly)
- Group migration handler (`migrate_to_chat_id`)
- `docs/llm/60_architecture.md` - architecture spec from implementation
- `.gitignore` update for Go artifacts

**Not tested end-to-end:** No real Telegram bot token has been used. All code compiles and storage tests pass, but handler logic is untested against real API.

## Immediate next steps (pick one)

### Option A: Domain tests
Write tests for each domain: profile FSM flow, stats buffer merge, moderation 3-strike. Mock TelegramAPI interface. ~2h.

### Option B: End-to-end test with real bot
Set TG_BOT_TOKEN, run bot, test manually in a real supergroup. Fix issues found. ~1h.

### Option C: Phase 7 polish
Add setMyCommands, onboarding handler, rate limiter, migration handler. ~1.5h.

### Option D: Architecture doc
Write `docs/llm/60_architecture.md` from the actual implementation. Capture decisions that diverged from the plan. ~30min.

## Read before starting

1. This file
2. `docs/llm/10_scope.md` - what's in scope
3. Architecture plan at `~/.claude/plans/quirky-marinating-glacier.md` - original design with critic findings

## Anti-patterns

1. **telego methods require `context.Context` as first arg.** Every API call: `bot.SendMessage(ctx, params)`, not `bot.SendMessage(params)`.
2. **telego Predicate signature:** `func(ctx context.Context, update telego.Update) bool` - not just `func(update) bool`.
3. **telego Use() takes Handler, not a middleware wrapper.** Middleware = Handler that calls `ctx.Next(update)`.
4. **MemberUser() returns `telego.User` (value), not pointer.** Cannot compare to nil.
5. **ChatPermissions fields are `*bool`, not `bool`.** Must create bool vars and pass pointers.
6. **stats counting must be middleware (Use), not handler.** telego routes to first match only - a handler would consume the update.
