---
id: deployment
kind: guide
---

# Deployment

Production runs as a docker-compose stack. Single replica, named
volume for bbolt, internal healthcheck (no host port published),
non-root container with tini as PID 1.

## Prerequisites

- Linux host with Docker 24+ and Compose v2 (`docker compose version`).
- A bot token from `@BotFather`.
- `/setprivacy` Disabled and bot re-added to the chat (otherwise the
  bot only sees commands and @-mentions; stats and membership stay
  empty). `/setinline` Enabled with a placeholder string.

## Image

`Dockerfile` in the repo root. Multi-stage:

- `golang:1.26-alpine` build stage. `CGO_ENABLED=0` because every
  dependency is pure Go. Build cache mounted via BuildKit so warm
  builds stay fast.
- `alpine:3.20` runtime. Ships `bidlobot`, `bidlobot-backup`, and
  `bidlobot-probe` into `/usr/local/bin`. Adds `ca-certificates`,
  `tzdata`, `wget` (for the healthcheck), `tini` (PID 1).
- Runs as `bidlobot` (UID 65532), `WORKDIR /var/lib/bidlobot`. A
  baked `.keep` marker forces a fresh named volume to inherit the
  image's `0750 bidlobot:bidlobot` ownership.
- `HEALTHCHECK` polls `http://127.0.0.1:8080/health` over the container
  loopback every 30 s, with 60 s start-period to absorb slow Telegram
  cold starts.

`docker compose build` produces `bidlobot:latest`. Tag explicitly with
`VERSION=v1.0.0 docker compose build` to bake the version into both
the image tag and the `--version` banner via ldflags.

## Compose stack

`docker-compose.yml`:

- Single service `bot`. `container_name: bidlobot`.
- `restart: unless-stopped`.
- `env_file: ./env` -- compose reads the env file alongside the
  compose YAML. On the deploy host this lives at `/opt/bidlobot/env`.
- `volumes: bidlobot-data:/var/lib/bidlobot` -- bbolt persists across
  container recreate.
- `stop_grace_period: 30s` -- App.ShutdownTimeout (10 s) for handler
  drain + 10 s for in-flight WaitGroup + 10 s slack for `bbolt.Close`.
- `healthcheck: wget /health` -- internal only. The host's 8080 / 8081
  are deliberately unmapped because they are usually occupied by other
  services on the deploy host.
- Resource caps: 256 MB memory, 0.5 CPU. Comfortably above the bot's
  steady-state footprint.
- JSON log rotation: 10 MB x 5 files = 50 MB ceiling per container.

## Environment

Required:

- `TG_BOT_TOKEN` -- format `\d+:[A-Za-z0-9_-]{35,}`. The bot validates
  this at startup and exits non-zero on bad shape.

Optional:

- `LOG_LEVEL` -- `debug` | `info` | `warn` | `error`, default `info`.
- `DB_PATH` -- bbolt directory. Container default `/var/lib/bidlobot`.
  Do not override unless you also rewire the volume mount.
- `HEALTH_PORT` -- container port for `/health` and `/version`,
  default `8080`. `0` disables the listener (and breaks the compose
  healthcheck unless you also rewrite the `test:` field).
- `RECORD_UPDATES` -- JSONL path inside the container; if set, every
  incoming update is appended for offline replay. Ship as a bind mount
  if you need to pull recordings from the host.

## First deploy

```sh
# On the deploy host
git clone https://github.com/veschin/bidlobot.git /opt/bidlobot
cd /opt/bidlobot
cp deploy/env.example env
$EDITOR env  # set TG_BOT_TOKEN

docker compose up -d --build
docker compose logs -f bot
```

Expect, in order:

1. `starting build=...`
2. `authenticated bot=<name> id=<n> can_read_all=true supports_inline=true`
3. `health server listening addr=:8080`
4. `bot started, polling for updates`

If `can_read_all=false`, fix BotFather then `docker compose restart bot`.

## Health and version

```sh
# Internal probe (compose healthcheck does this every 30 s)
docker exec bidlobot wget -qO- http://127.0.0.1:8080/health

# Build banner without execing
docker exec bidlobot bidlobot --version
```

`/health` returns `200 {"status":"ok"}` when:

- The bbolt instance accepts a no-op view transaction.
- The most recent update arrived within 5 minutes (or the bot is
  still inside its startup grace).
- A cached `getMe` (TTL 60 s) returned successfully.

`/version` returns build info including the commit hash injected at
build time via `-X main.version=... -X main.commit=...`.

## Backup

`deploy/backup.sh` -- host-side stop / cp / start. Resolves the
volume mount path via `docker volume inspect bidlobot-data`, copies
`bidlobot.db`, then restarts the bot. Trades ~10 s downtime for a
guaranteed-consistent snapshot.

Cron suggestion (root):

```cron
# Hot snapshot at 03:17 UTC daily, retain 7 newest by mtime.
17 3 * * * /opt/bidlobot/deploy/backup.sh >>/var/log/bidlobot-backup.log 2>&1
```

Default destination: `/var/backups/bidlobot/bidlobot-YYYYMMDD-HHMMSS.db`,
configurable via `BIDLOBOT_BACKUP_DIR`. Failed runs exit nonzero so
cron alerts.

> The earlier sidecar-style `bidlobot-backup` binary (still in the
> image, callable via `docker exec bidlobot bidlobot-backup`) cannot
> snapshot a running bot: bbolt holds an exclusive flock and the
> backup binary's read-only open times out. Use it only after stopping
> the bot.

## Logs

Structured JSON to stdout, captured by Docker's `json-file` driver.

```sh
docker compose logs -f bot
docker compose logs --since 1h bot | jq 'select(.level=="ERROR")'
```

What never leaks into logs:

- `TG_BOT_TOKEN` (telego's default replacer redacts; do not enable
  telego `WithDebug`, which prints raw payloads).
- Message text -- only `chat_id`, `user_id`, command, duration_ms.

What does:

- Authentication, BotFather flag state.
- Per-handler dispatch durations, API errors verbatim.
- Rate-limiter drops (`chat_id` + drop reason).
- Pending GC removed-count.

For longer retention forward the journal to a central log store via
`vector` or `systemd-journal-upload`.

## CI

`.github/workflows/ci.yml`:

- `go vet`, `go test -race -cover`, `go build` on every push and PR.
- `docker buildx build` (no push) so a Dockerfile regression fails
  CI before it bites a deploy.
- `gitleaks` scan to catch accidentally-committed env files or
  hard-coded tokens.

Coverage uploaded as artifact (7-day retention).

## Rollback

bbolt schema is forward-compatible (we never delete buckets, only
add). To roll back:

```sh
# On deploy host
cd /opt/bidlobot
git fetch
git checkout <previous-good-sha>
docker compose up -d --build
```

If the older binary doesn't recognize a bucket created by the newer
one, the bucket is ignored. For destructive-schema rollbacks (rare),
restore from a backup taken before the upgrade and stop the bot
before swapping the database file:

```sh
docker compose stop bot
VOL=$(docker volume inspect -f '{{.Mountpoint}}' bidlobot-data)
cp /var/backups/bidlobot/bidlobot-<ts>.db "$VOL/bidlobot.db"
docker compose start bot
```

## Operational footguns

- **Single token, two processes**: stop production before running
  `cmd/probe` or any local `go run ./cmd/bidlobot` against the same
  `TG_BOT_TOKEN`. Telegram returns 409 to the loser of the
  `getUpdates` race; both processes flap and split traffic.
- **Forgetting to remove-and-re-add after `/setprivacy` flip**:
  privacy mode is cached at join. The bot will start cleanly, polls
  successfully, but only sees commands and @-mentions until you
  remove and re-add it.
- **Editing `env` without restart**: compose only re-reads `env_file`
  on container recreate. After editing: `docker compose up -d`.
- **Backup during crash loop**: `deploy/backup.sh` exits nonzero if
  the container is not running, so cron alerts. Diagnose the crash
  first; do not wrap the script in `|| true`.
