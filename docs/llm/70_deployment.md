---
id: deployment
kind: guide
touches:
  - Dockerfile
  - docker-compose.yml
  - deploy/backup.sh
  - deploy/env.example
  - scripts/backup.sh
  - .github/workflows/ci.yml
  - .omp/skills/bidlobot-deploy/scripts/deploy.sh
  - .omp/skills/bidlobot-deploy/scripts/status.sh
  - cmd/bidlobot/
written: 2026-05-14
updated: 2026-09-12
---

# Deployment

Production runs as a docker-compose stack with a **single service**:
`bot` (the Go binary). Single bot replica (Telegram allows exactly one
getUpdates poller per token). Named volume for bbolt, internal
healthcheck (no host ports published), non-root container with tini as
PID 1. Revised 2026-08-16 (xpost rework: the `xshot` Puppeteer sidecar
was retired; the bot resolves X posts via the FixTweet API and
downloads twimg media directly).

## Prerequisites

- Linux host with Docker 24+ and Compose v2 (`docker compose version`).
- A bot token from `@BotFather`.
- The **owner's Telegram user ID** (`BOT_OWNER_ID`) - required at
  startup; only this user may add the bot to a chat.
- `/setinline` Enabled with a placeholder string (read-only launcher).
- `/setprivacy` - a **hard prerequisite now**: the content features
  (YouTube `si=` sanitizer, TikTok repost, Instagram repost, X-post
  repost, summarize recorder) all read full message text, so privacy
  must be **OFF**
  (`@BotFather` -> `/setprivacy` -> Disable) and the bot **removed +
  re-added** to every chat (privacy is cached at join). With privacy
  ON the bot only sees commands/@-mentions/replies, and all content
  middlewares are silent.

## Image

`Dockerfile` in the repo root. Multi-stage:

- `golang:1.26-alpine` build stage. `CGO_ENABLED=0` (every Go
  dependency is pure Go). Build cache via BuildKit. Builds `bidlobot`,
  `bidlobot-backup`, `bidlobot-probe` into `/out`.
- `debian:bookworm-slim` runtime. Installs `bash ca-certificates curl
  ffmpeg tini tzdata unzip wget` (ffmpeg/ffprobe for the TikTok
  audio check), **yt-dlp pinned release** (2026.03.17, sha256-checked
  at build; a newer release can be pinned via `YT_DLP_VERSION` arg -
  the pin holds until upstream fixes the TikTok extractor regression,
  yt-dlp issue #17403), and **Bun 1.3.14 +
  `@oh-my-pi/pi-coding-agent` 16.3.6** (the `omp` CLI on PATH,
  version-checked at build). yt-dlp is the **fallback** TikTok source;
  the primary path is the tikwm mirror
  ([56_tiktok_repost.md](56_tiktok_repost.md)), and it is the **only**
  source for the Instagram repost
  ([61_instagram_repost.md](61_instagram_repost.md)). Runs as `bidlobot`
  (UID 65532), `WORKDIR /var/lib/bidlobot`, tini as PID 1,
  HEALTHCHECK on the loopback `/health`.

The `bidlobot-backup` binary stays in the image but cannot snapshot a
running bot (bbolt exclusive flock) - use `deploy/backup.sh`
(stop/cp/start) instead.

## Compose stack

`docker-compose.yml`:

- **bot**: `container_name: bidlobot`, `restart: unless-stopped`,
  `env_file: ./env` (on the deploy host: `/opt/bidlobot/env`) **plus**
  `environment: - DEEPSEEK_API_KEY` (taken from the host shell env -
  the omp CLI reads it directly, the Go binary never sees it).
  `volumes: bidlobot-data:/var/lib/bidlobot`; `stop_grace_period: 30s`
  (10s handler drain + 10s in-flight + 10s bbolt close slack);
  healthcheck `wget --spider http://127.0.0.1:8080/health` every 30s,
  60s start-period; resource caps 256 MB / 0.5 CPU; JSON log rotation
  10 MB x 5.

Host ports 8080/8081 are deliberately unmapped (occupied by other
services on the deploy host); all probes go over container loopback.

**Resolver override**: the `bot` service pins
`dns: [192.168.0.1, 1.1.1.1, 8.8.8.8]`. Without it the container
resolves through Docker's embedded resolver -> `127.0.0.53`
(systemd-resolved) -> the router, and host-side `127.0.0.53` stalls in
10-minute bursts (the cause of the DNS-failure class in the TikTok and
Telegram error counts). `.lan` names still resolve because the router
answers them. Applied with `docker compose up -d bot` (recreate).

## Environment

Required:

- `TG_BOT_TOKEN` -- format `\d+:[A-Za-z0-9_-]{35,}`; validated at
  startup, exit non-zero on bad shape.
- `BOT_OWNER_ID` -- positive int64 Telegram user id. Only this user
  may add the bot to a supergroup; a non-owner add triggers an
  immediate `LeaveChat` + owner DM notice. Missing/invalid ->
  startup error.

Optional:

- `LOG_LEVEL` -- debug|info|warn|error, default `info`.
- `DB_PATH` -- bbolt directory. Container default `/var/lib/bidlobot`.
- `HEALTH_PORT` -- /health + /version port, default `8080`; `0`
  disables the listener (breaks the compose healthcheck unless
  rewritten).
- `PI_BINARY` / `PI_MODEL` -- the summarize provider CLI + model.
  Defaults `omp` and `deepseek/deepseek-v4-flash`. The `omp` binary is
  inside the image; a missing binary is a startup failure. Production
  runs `deepseek/deepseek-flash` (the v4.1 flash model of the DeepSeek
  API).
- `DEEPSEEK_API_KEY` -- provider credential for the omp CLI. NOT read
  by the Go binary; compose forwards it from the host environment.
  `deploy.sh` pipes it from `pass show token/deepseek` over SSH.
  **A manual `docker compose up -d` outside `deploy.sh` starts the
  container without it** (the value is a pass-through, not a file
  entry): summarization then fails with `No API key found for
  deepseek`. Recreate through the deploy script, or export the key from
  `pass` into the same shell first.
- `CAPTCHA_ENABLED` (default false) / `CAPTCHA_TIMEOUT` (default `1m`,
  1m..30m) -- new-member math captcha + welcome animation.
- `INSTAGRAM_PROXY` / `INSTAGRAM_COOKIES` -- egress for the Instagram
  repost download ([61_instagram_repost.md](61_instagram_repost.md)).
  Several ISPs block Instagram outright (Russia blocks it at the IP
  level) and a growing share of posts is login-walled, so the yt-dlp
  call accepts an HTTP/SOCKS proxy (`socks5h://` resolves DNS through
  the proxy) and a Netscape-format cookie jar. Both are validated at
  startup and default to unset: direct, anonymous. With a blocked
  egress and neither set, every Instagram link fails into the deferred
  queue.
- `CLEANUP_DAILY_AT` / `CLEANUP_GRACE` / `CLEANUP_DAILY_BATCH` --
  legacy gracekick tuning, still validated, **but the scheduler is
  idle** (no `/cleanup` command exists to seed a campaign). Harmless
  to keep or drop.

Removed env (no longer read; stale values are ignored): `GLM_API_KEY`,
`GLM_BASE_URL`, `GLM_MODEL`, `RECORD_UPDATES`, `CLEANUP_DAILY_ENABLED`,
`CLEANUP_DAILY_THRESHOLD`.

## First deploy

```sh
# On the deploy host
git clone https://github.com/veschin/bidlobot.git /opt/bidlobot
cd /opt/bidlobot
cp deploy/env.example env
$EDITOR env  # set TG_BOT_TOKEN + BOT_OWNER_ID

docker compose up -d --build
docker compose logs -f bot
```

Expect, in order:

1. `starting build=...`
2. `authenticated bot=<name> id=<n> can_read_all=<true|false> supports_inline=true`
3. `health server listening addr=:8080`
4. `bot started, polling for updates`

`can_read_all=true` is required for the content features (privacy OFF
+ re-add). If it reads `false`, flip BotFather privacy and re-add the
bot.

## Upgrade (routine)

`origin/master` is the deploy ref. Standard upgrade:

```sh
cd /opt/bidlobot
git fetch origin && git checkout master && git pull --ff-only
docker compose up -d --build
docker compose logs -f bot
```

Deploy helper: `.omp/skills/bidlobot-deploy/scripts/deploy.sh`
(git push -> SSH with `DEEPSEEK_API_KEY` from `pass show
token/deepseek` -> `docker compose up -d --build` -> health wait) and
`status.sh` (env + container status). NOTE: `status.sh` still greps
`GLM_` in the host env - stale, cosmetic only.

Verifiable upgrade facts:

- **New buckets are idempotent** (`CreateBucketIfNotExists`), no DB
  migration step. Rollback stays forward-compatible (unknown buckets
  ignored by older binaries).
- The image build pins yt-dlp and omp versions; a build failure
  (sha256 mismatch, missing binary) fails CI / the deploy build before
  it reaches prod.

## Health and version

```sh
docker exec bidlobot wget -qO- http://127.0.0.1:8080/health
docker exec bidlobot bidlobot --version
```

`/health` returns `200 {"status":"ok"}` when: bbolt accepts a no-op
view tx; the most recent update arrived within 5 min (or startup
grace); a cached `getMe` (TTL 60s) succeeded. `/version` returns build
info (commit hash via `-X main.version=... -X main.commit=...`).

## Backup

`deploy/backup.sh` -- host-side stop / cp / start. Resolves the
volume mount via `docker volume inspect bidlobot-data`, copies
`bidlobot.db`, restarts the bot. ~10s downtime for a guaranteed-
consistent snapshot.

The script **must run as root**, for three independent reasons: it
writes into the root-owned `/var/backups`; it defaults `COMPOSE_DIR` to
`/opt/bidlobot` while the checkout lives in `/home/veschin/bidlobot`;
and the resolved source path under `/var/lib/docker/volumes` is not
readable by the deploy user. Run as a normal user it stops the bot,
prints `ERROR: /var/lib/docker/volumes/bidlobot-data/_data/bidlobot.db
missing; restarting bot then aborting` (verified 2026-09-12) and starts
the bot again - no backup, no harm.

Install as root:

```cron
17 3 * * * cd /home/veschin/bidlobot && BIDLOBOT_COMPOSE_DIR=/home/veschin/bidlobot ./deploy/backup.sh >> /var/log/bidlobot-backup.log 2>&1
```

Run it by hand once before trusting the cron:

```sh
sudo sh -c 'cd /home/veschin/bidlobot && BIDLOBOT_COMPOSE_DIR=/home/veschin/bidlobot ./deploy/backup.sh'
```

Without root, the same stop/copy/start works through the docker socket,
which is how `docker cp` reads the file out of the container:

```sh
docker stop bidlobot
docker cp bidlobot:/var/lib/bidlobot/bidlobot.db \
    "/home/veschin/bidlobot-backups/bidlobot-$(date -u +%Y%m%d-%H%M%S).db"
docker start bidlobot
```

Default destination `/var/backups/bidlobot/...`, configurable via
`BIDLOBOT_BACKUP_DIR`; 7 files kept. Failed runs exit nonzero so cron
alerts.

**Status 2026-09-12**: no cron entry had ever been installed and no
backup existed. The first consistent snapshot was taken by hand with
the `docker cp` recipe above (2097152 bytes, bbolt page magic
`ed0cdaed` at offset 16) into `/home/veschin/bidlobot-backups/`.

## Logs

Structured JSON to stdout, Docker `json-file` driver.

```sh
docker compose logs -f bot
docker compose logs --since 1h bot | jq 'select(.level=="ERROR")'
```

What never leaks into logs: `TG_BOT_TOKEN` (telego's default replacer
redacts; never enable `WithDebug`), `DEEPSEEK_API_KEY` (the omp CLI
reads it from env; the Go binary never logs it), message text (only
chat_id/user_id/command/duration_ms). The `/summarize` feature *sends*
recent message text to the external DeepSeek provider via omp (privacy
note in [45_summarize.md](45_summarize.md)) but never writes it to
disk or logs (memfd transcript, no temp file).

## CI

`.github/workflows/ci.yml`: `go vet`, `go test -race -cover`, `go
build` on push/PR; `docker buildx build` (no push) so Dockerfile
regressions fail CI; `gitleaks` scan. Coverage artifact 7-day.

## Rollback

bbolt schema is forward-compatible (buckets only ever added). To roll
back:

```sh
cd /opt/bidlobot
git fetch
git checkout <previous-good-sha>
docker compose up -d --build
```

If the older binary doesn't recognize a newer bucket, it is ignored.
For destructive-schema rollbacks (rare), restore from backup with the
bot stopped:

```sh
docker compose stop bot
VOL=$(docker volume inspect -f '{{.Mountpoint}}' bidlobot-data)
cp /var/backups/bidlobot/bidlobot-<ts>.db "$VOL/bidlobot.db"
docker compose start bot
```

## Operational footguns

- **Single token, two processes**: stop production before running
  `cmd/probe` or any local `go run ./cmd/bidlobot` against the same
  `TG_BOT_TOKEN` (409 getUpdates race).
- **Forgetting to remove-and-re-add after `/setprivacy` flip**:
  privacy is cached at join; content middlewares stay silent until the
  bot is removed and re-added.
- **Editing `env` without restart**: compose only re-reads `env_file`
  on container recreate. `DEEPSEEK_API_KEY` comes from the host shell
  env at `up` time - after changing it, `docker compose up -d` (not
  just restart) to re-read both.
- **Backup during crash loop**: `deploy/backup.sh` exits nonzero if
  the container is not running; diagnose first, do not wrap in
  `|| true`.
- **fxtwitter unreachable**: X-post reposts decline with a random
  phrase (original message kept), other features unaffected. The
  FixTweet API is a public third-party service; sustained outages
  surface as `xpost: metadata fetch failed` in the logs.
