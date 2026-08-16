# BidloBot

Telegram bot for managing IT-community supergroups. One Go binary +
embedded bbolt database, long-polling. Ships as a docker-compose stack
(single service).

## What it does

| Capability | Surface |
|------------|---------|
| Statistics | `/stats`, `/stats top`, `/stats today`, `/stats month(s)`, `/stats @user` - public in the group |
| Mini-games | `/dice`, `/battle`, `/quiz`, `/poll`, `/8ball`, `/guess`, `/hangman`, `/duel`, `/trivia` - public, per-user cooldown |
| Reputation | `/praise @user`, `/roast @user`, `/rep`, `/reptop` - durable per-chat balance |
| Referral catalog | `/refs`, `/refreg`, `/refreport` (admin) - chat-scoped referral links |
| YouTube sanitizer | strips `si=` share-tracking param (delete + attributed repost) |
| TikTok repost | downloads TikTok video, reposts attributed, deletes original |
| X/Twitter repost | re-sends an X post as one message (text + native media + canonical link, via the FixTweet API), deletes original |
| Chat summarization | admin `/summarize [N]` (alias `/итог`) - DeepSeek V4 Flash via OMP/Pi, weighted digest with cost footer |
| New-member captcha | opt-in (`CAPTCHA_ENABLED`): math challenge, kick on wrong/no answer, welcome animation on solve |
| Admission gate | only `BOT_OWNER_ID` may add the bot; non-owner adds trigger LeaveChat |
| Deferred retries | `/flush` - retry your failed TikTok exports / summarize calls (48h window) |
| Owner console | `/chats` in DM - list chats, revoke ("Отозвать") |

Inline (`@bidlobot ...`) is a read-only launcher (stats/games/help)
only. There is **no moderation surface** - the DM moderation console
(`/warn /mute /ban /cleanup /import`) was removed in 2026-06.

Read-only members (react-only) are preserved: reactions count as
activity.

## Architecture, deployment, manual verification

The full reference lives in `docs/llm/`:

- `00_index.md` - table of contents.
- `10_scope.md` - current scope, command surfaces, removed features, env.
- `45_summarize.md` - the OMP/Pi summarization provider + budget model.
- `60_architecture.md` - layered composition, middleware order, bbolt
  schema, invariants, failure matrix.
- `70_deployment.md` - docker-compose stack (single bot service), env
  vars, healthcheck, backup, rollback.
- `handoff.md` - current state + manual smoke checklist.

## Quick start (local dev)

Requires Go 1.26+, `yt-dlp`, `ffmpeg`, and the `omp` CLI on PATH, plus
a bot token from `@BotFather`.

```sh
# Build and run with env
TG_BOT_TOKEN=... BOT_OWNER_ID=... DEEPSEEK_API_KEY=... DB_PATH=./data go run ./cmd/bidlobot
```

`BOT_OWNER_ID` is required. `DEEPSEEK_API_KEY` is read by the `omp`
CLI (the Go binary never parses it); without it `/summarize` fails
provider-side.

If `can_read_all=false`: `@BotFather` -> `/setprivacy` -> off, then
**remove and re-add the bot** to every chat (privacy is cached at
join). The content features (sanitizer, TikTok, xpost, summarize
recorder) need message content.

> Important: only one process per token can poll `getUpdates` at a time.
> Stop any production deployment before starting a local instance with the
> same token, otherwise updates are split between processes.

## Quick start (production, docker)

```sh
# 1. Build the image
docker compose build

# 2. Drop the env file alongside docker-compose.yml
cp deploy/env.example ./env
$EDITOR ./env  # set TG_BOT_TOKEN + BOT_OWNER_ID

# 3. Start
docker compose up -d
docker compose logs -f bot
```

The stack runs non-root (UID 65532), tini as PID 1, health endpoints
on container loopbacks only (no host ports), bbolt in the
`bidlobot-data` named volume. See `docs/llm/70_deployment.md`.

## Tests

```sh
go test -race ./...     # 29 packages (23 with tests)
```

Replay tests against recorded sessions in `internal/bot/replay_test.go`.

## Layout

```
cmd/
  bidlobot/        production entrypoint
internal/
  bot/             routes, middleware (observers + sanitizer/reposters),
                   games, referral, reputation, captcha, summarize,
                   deferred queue, inline, legacy dispatcher
  domain/          membership / stats / monthstats / captcha / summarize /
                   referral / reputation / cleanup / gracekick / moderation
  games/           dice / battle / duel / guess / hangman / quiz ...
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
Dockerfile         multi-stage, debian runtime, yt-dlp + ffmpeg + omp
docker-compose.yml single bot service, named volume, internal healthcheck
```

## License

Internal.
