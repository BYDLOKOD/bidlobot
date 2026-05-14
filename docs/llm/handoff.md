---
id: handoff
kind: guide
---

# Handoff: next session action plan

Last updated: 2026-05-14, after Phase 0 of group-management pivot.

## Current state

Branch: `master`. Commits:
- `9851c0b` - refactor: snapshot Go rewrite, remove Clojure prototype (tag `v0-bio-archive`, branch `archive/profiles-bio` points here)
- *(pending)* - Phase 0: archive bio domain

Six implementation phases planned:
- **Phase 0 (current)** - archive bio, clean docs
- **Phase 1** - membership tracking (chats bucket, member upserts on message/reaction/chat_member)
- **Phase 2** - inline mode commands (`@bidlobot ...` autocomplete + preview-confirm pattern)
- **Phase 3** - cleanup feature (kick inactive who never wrote and never reacted)
- **Phase 4** - mini-games (dice, reaction-battle, code-quiz)
- **Phase 5** - production-readiness (rate limiter, retry, migration, /health, CI, backup)
- **Phase 6** - integration testing (replayer + smoke tests)

## What exists now

- 22 Go files across `cmd/bidlobot`, `internal/{bot,domain/{stats,moderation},shared,storage,testutil,text}`
- Three subscription types: `message`, `callback_query`, `my_chat_member`, `chat_member`
- bbolt with 6 buckets (`profiles`, `profiles_by_chat` kept empty for archive revival; `stats`, `stats_by_chat`, `warnings`, `warns_by_target`)
- AdminCache (60 s TTL, invalidated on `chat_member` updates)
- `RECORD_UPDATES=path` env activates JSONL recorder middleware
- `go build` clean, `go vet` clean, `go test` green (storage, stats, moderation, shared)

## Test environment

User has a test supergroup with the bot already added as administrator with `can_restrict_members`. `TG_BOT_TOKEN` is in `.env`. Bot reads all messages (privacy disabled).

## What does NOT exist

- Chats registry (no list of installations)
- Membership tracking (no per-user last_seen / last_message / last_reaction)
- Inline mode (`HandleInlineQuery` not registered)
- Cleanup feature
- Mini-games
- `message_reaction` subscription (requires admin + explicit allowed_updates)
- Rate-limited sender wrapper
- Migration handler (`migrate_to_chat_id`)
- Healthcheck endpoint
- CI pipeline
- Backup tooling
- End-to-end run against the real bot

## Anti-patterns (carry-over)

1. **telego methods require `context.Context` as first arg.** Every API call: `bot.SendMessage(ctx, params)`.
2. **telego `Predicate` signature:** `func(ctx context.Context, update telego.Update) bool`.
3. **telego `Use()` takes Handler, not a middleware wrapper.** Middleware = Handler that calls `ctx.Next(update)`.
4. **`MemberUser()` returns `telego.User` (value), not pointer.** Cannot compare to nil.
5. **`ChatPermissions` fields are `*bool`, not `bool`.** Must create bool vars and pass pointers.
6. **stats counting must be middleware (`Use`), not handler.** telego routes to first match only.

## Anti-patterns (new constraints from group-management scope)

7. **Bot API has no `getChatMembers`.** Member list is built bottom-up from observed events.
8. **`message_reaction` updates require bot admin + explicit `"message_reaction"` in `allowed_updates`.** Reactions from other bots are filtered.
9. **`InlineQuery` carries no `chat_id`** - only `chat_type`. Defer admin checks to callback after preview is selected.
10. **Inline result is sent as the user's message** with attached `reply_markup`. Callback handlers run with `query.From` = user, `query.Message.Chat.ID` = real chat - both required for admin verification before destructive action.
11. **Kick = `banChatMember` then `unbanChatMember(only_if_banned=true)`.** Without the unban step the user is permanently banned.
12. **Cleanup preview must declare the observation window.** Bot only knows about users it has observed; users silent since before bot installation are invisible. Include this in every preview message.
