---
id: handoff
kind: guide
---

# Handoff: next session action plan

Last updated: 2026-05-14, after Phases 0-2.

## Current state

Branch: `master`.

Six implementation phases planned:
- **Phase 0 done** - archive bio, clean docs
- **Phase 1 done** - membership tracking (chats bucket, member upserts on message/reaction/chat_member); stats now prints real names; moderation refuses to act on unknown @usernames
- **Phase 2 done** - inline mode as a slash-command launcher (read-only commands only); destructive deferred to Phase 3
- **Phase 3** - cleanup feature + pending-actions storage + destructive inline confirms
- **Phase 4** - mini-games (dice, reaction-battle, code-quiz)
- **Phase 5** - production-readiness (rate limiter, retry, migration, /health, CI, backup)
- **Phase 6** - integration testing (replayer + smoke tests)

## BotFather one-time setup

For inline mode to work the bot owner must run, once, in @BotFather:
1. `/setinline` -> choose the bot -> enter placeholder text (e.g. `stats top, warns @user, help`)
2. `/setinlinefeedback` -> Disabled (we don't process chosen_inline_result yet)
3. Optionally `/setprivacy` -> Disabled (so the bot reads all messages - required for stats and membership tracking; the test chat is already configured this way)

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
