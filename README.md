# BidloBot

Telegram bot for managing IT-community supergroups. One Go binary, embedded
bbolt database, long-polling.

## What it does

| Capability | Surface |
|------------|---------|
| Statistics | `/stats`, `/stats top`, `/stats today`, `/stats @user` |
| Inactive cleanup | `@bidlobot cleanup 6mo` -> preview with candidate list -> confirm -> kick (ban + immediate unban) |
| Moderation | `/warn`, `/warns`, `/mute`, `/unmute`, `/ban`, `/unban` (admins only) |
| Inline launcher | `@bidlobot ...` autocomplete for every command above |
| Mini-games | `/dice`, `/battle X Y`, `/quiz` (and `/quiz top`) |
| Membership tracking | message + reaction observers; powers cleanup and stats |

Read-only members (those who only react) are **preserved** during cleanup -
the bot treats reactions as activity.

## Architecture, deployment, manual verification

The full reference lives in `docs/llm/`:

- `00_index.md` - the table of contents.
- `10_scope.md` - what's in scope, what was archived, ID conventions.
- `30_stats.md`, `40_moderation.md` - domain rules.
- `50_telegram.md` - Telegram API specifics that shape the design.
- `60_architecture.md` - layered composition, bbolt schema, key invariants,
  failure handling matrix, where to add features.
- `70_deployment.md` - build flags, env vars, systemd unit, backup cron,
  healthcheck, BotFather setup, rollback.
- `handoff.md` - current session state + 14-step manual smoke checklist.

## Quick start

Requires Go 1.26+ and a bot token from `@BotFather`.

```sh
# Validate token + BotFather config
go run ./cmd/probe          # expects can_read_all=true, supports_inline=true

# Build
go build -o bidlobot ./cmd/bidlobot

# Run with token in env
TG_BOT_TOKEN=... DB_PATH=./data ./bidlobot
```

If `can_read_all=false`: `@BotFather` -> `/setprivacy` -> off, then **remove and
re-add the bot** to every chat.

If `supports_inline=false`: `@BotFather` -> `/setinline` -> on with placeholder
text.

## Verifying in a real chat

```sh
INTEGRATION_TEST=1 SMOKE_TIMEOUT=600 go run ./cmd/smoke
```

Watch the JSON log on stdout. Send commands from the chat following the
14-step checklist in `docs/llm/handoff.md` `## Manual smoke checklist`.

## Tests

```sh
go test -race ./...     # 14 packages, 100+ tests
```

End-to-end coverage (inline -> callback dispatcher -> executor -> bbolt) lives
in `internal/bot/end_to_end_test.go`. Replay tests against recorded sessions
in `internal/bot/replay_test.go`.

## Layout

```
cmd/
  bidlobot/        production entrypoint
  bidlobot-backup/ online bbolt backup
  probe/           one-shot getMe (no polling, no side effects)
  smoke/           bounded production wiring against real chat
internal/
  bot/             telego dispatch, middleware, dispatcher, executors
  domain/          membership / stats / moderation / cleanup / pending
  games/           dice / battle / quiz
  shared/          admin cache, format, target resolve, telegram interface
  shared/ratelimit per-chat outgoing token bucket
  shared/retry     429+5xx retry policy
  shared/tgclient  composed wrapper: migration -> retry -> rate-limit
  storage/         bbolt repos, key conventions, group->supergroup migration
  testutil/        MockAPI, recorder, update factories
  text/            Russian user-facing strings
```

## License

Internal.
