# BidloBot

Telegram bot for managing IT-community supergroups. One Go binary, embedded
bbolt database, long-polling. Ships as a docker-compose stack.

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
- `70_deployment.md` - docker-compose stack, env vars, healthcheck, BotFather
  setup, backup, rollback.
- `handoff.md` - current session state + manual smoke checklist.

## Quick start (local dev)

Requires Go 1.26+ and a bot token from `@BotFather`.

```sh
# Validate token + BotFather config
go run ./cmd/probe          # expects can_read_all=true, supports_inline=true

# Build and run with token in env
TG_BOT_TOKEN=... DB_PATH=./data go run ./cmd/bidlobot
```

If `can_read_all=false`: `@BotFather` -> `/setprivacy` -> off, then **remove and
re-add the bot** to every chat.

If `supports_inline=false`: `@BotFather` -> `/setinline` -> on with placeholder
text.

> Important: only one process per token can poll `getUpdates` at a time.
> Stop any production deployment before starting a local instance with the
> same token, otherwise updates are split between processes.

## Quick start (production, docker)

```sh
# 1. Build the image
docker compose build

# 2. Drop the env file alongside docker-compose.yml
cp deploy/env.example ./env
$EDITOR ./env  # set TG_BOT_TOKEN

# 3. Start
docker compose up -d
docker compose logs -f bot
```

The image runs as non-root (UID 65532), uses tini as PID 1 for clean
SIGTERM handling, exposes the health endpoint on the container's loopback
only (no host port mapping), and persists bbolt data in the `bidlobot-data`
named volume. See `docs/llm/70_deployment.md` for the full deploy runbook.

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
  bidlobot-backup/ online bbolt snapshot binary (used inside container)
  probe/           one-shot getMe (no polling, no side effects)
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
deploy/
  env.example      template for the operator env file
  backup.sh        host-side stop/cp/start backup wrapper
Dockerfile         multi-stage, alpine runtime, non-root, tini PID 1
docker-compose.yml single-service stack, named volume, internal healthcheck
```

## License

Internal.
