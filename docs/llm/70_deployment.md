---
id: deployment
kind: guide
---

# Deployment

What to copy where on a Linux box to run the bot 24/7. Adapted to Debian/Ubuntu but everything is plain shell + systemd.

## Build

```sh
GOFLAGS="-trimpath" go build -ldflags "
  -X main.version=$(git describe --tags --always)
  -X main.commit=$(git rev-parse HEAD)
" -o /usr/local/bin/bidlobot ./cmd/bidlobot

go build -o /usr/local/bin/bidlobot-backup ./cmd/bidlobot-backup
go build -o /usr/local/bin/bidlobot-probe ./cmd/probe
```

`bidlobot --version` prints the build banner. `bidlobot --check-config` validates env vars and exits 0/1 without opening the database.

## Environment

Required:

- `TG_BOT_TOKEN` - must match `\d+:[A-Za-z0-9_-]{35,}`. Store in `/etc/bidlobot/env` (mode 0600, owned by the bot user) and `EnvironmentFile=` it from systemd.

Optional:

- `DB_PATH` - bbolt directory, default `./data`. Use `/var/lib/bidlobot` in production.
- `LOG_LEVEL` - `debug`/`info`/`warn`/`error`, default `info`.
- `HEALTH_PORT` - HTTP listener for `/health` and `/version`, default `8080`. Set `0` to disable.
- `RECORD_UPDATES` - JSONL path; if set, every incoming update is appended for offline replay.

## systemd unit

`/etc/systemd/system/bidlobot.service`:

```ini
[Unit]
Description=BidloBot - Telegram group management
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=bidlobot
Group=bidlobot
EnvironmentFile=/etc/bidlobot/env
ExecStart=/usr/local/bin/bidlobot
Restart=on-failure
RestartSec=5s

# Security hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=/var/lib/bidlobot

# Resource caps (tune for your scale)
MemoryMax=256M
TasksMax=128

# Graceful shutdown matches App.ShutdownTimeout (10s) + slack
TimeoutStopSec=15s
KillMode=mixed

[Install]
WantedBy=multi-user.target
```

Enable + start:

```sh
useradd --system --home /var/lib/bidlobot --shell /usr/sbin/nologin bidlobot
install -d -o bidlobot -g bidlobot /var/lib/bidlobot
install -d -m 0750 -o root -g bidlobot /etc/bidlobot
install -m 0640 -o root -g bidlobot env_template /etc/bidlobot/env
systemctl daemon-reload
systemctl enable --now bidlobot
journalctl -u bidlobot -f
```

## Backup

`scripts/backup.sh` is in the repo. Drop it on the host and add a cron entry. The script prefers the Go binary (true online snapshot via `db.View(tx -> tx.WriteTo)`); if the bot holds the exclusive write lock, it falls back to `cp` with bbolt's torn-meta tolerance and warns. Both paths leave the result at `backups/bidlobot-YYYYMMDD-HHMMSS.db`.

`/etc/cron.d/bidlobot-backup`:

```cron
# Hot bbolt snapshot every hour, retain last 7 by mtime.
17 * * * * bidlobot /usr/local/bin/bidlobot-backup -src /var/lib/bidlobot/bidlobot.db -dst /var/lib/bidlobot/backups/ >/dev/null 2>&1
```

For point-in-time copies, stop the bot first; see the script header for the tradeoff.

## Health

`HEALTH_PORT` defaults to `8080`. Probe from monitoring:

```sh
curl --silent --max-time 3 http://localhost:8080/health
# {"status":"ok"} -> 200
# {"status":"degraded","reason":"db_closed"} -> 503
```

Hook into Prometheus blackbox or a vanilla cron:

```sh
*/1 * * * * bidlobot bash -c 'curl -fsS --max-time 3 http://localhost:8080/health || systemctl restart bidlobot'
```

`/version` returns build info, useful for confirming a deploy:

```sh
curl http://localhost:8080/version
```

## Logs

Structured JSON to stdout. `journalctl -u bidlobot -o cat | jq` for tailing. Recommend forwarding to your central log store via `systemd-journal-remote` or `vector`.

What never leaks into logs:
- `TG_BOT_TOKEN`
- Message text (only chat_id, user_id, command, duration_ms)
- bio / profile content (the bio domain is archived anyway)

What does:
- chat_id (public)
- user_id (public, stable)
- handler dispatch durations
- API errors verbatim
- Rate-limiter drops (chat_id + drop reason)
- Pending GC removed-count

## CI

`.github/workflows/ci.yml` runs `go vet`, `go test -race -cover`, `go build` on every push and PR. Use Go 1.26+. The cache key is the module hash, so dependency bumps refresh automatically.

For deployments via tag pushes, add a release workflow that runs `goreleaser` or `GOOS=linux go build` and uploads the binary as a release artifact; see `.github/workflows/ci.yml` for the setup-go pattern.

## BotFather one-time setup

Identical to `handoff.md` but reproduced here so this file is self-contained:

1. `@BotFather` -> `/mybots` -> bot name.
2. `Bot Settings` -> `Group Privacy` -> **Turn off**. Then **remove and re-add the bot to every chat** (privacy is cached at join).
3. `Bot Settings` -> `Inline Mode` -> **Turn on** -> placeholder text e.g. `stats top, cleanup 6mo, warn @user`.
4. `Bot Settings` -> `Inline Feedback` -> **Disabled**.
5. Promote bot to administrator in every chat where you want stats / cleanup / moderation. Minimum permission: `Restrict Members`. Add `Delete Messages` if you also want delete features later.
6. Verify: `bidlobot-probe` reports `can_read_all=true, supports_inline=true`.

Skipping any of 2-5 silently disables that capability - the bot will start and answer commands but stats/membership/cleanup will be empty.

## Rollback

The bot stores state in bbolt. Forward-compatible schema changes only (we never delete buckets, only add). To roll back to an older binary:

1. `systemctl stop bidlobot`
2. Replace `/usr/local/bin/bidlobot` with the older binary.
3. `systemctl start bidlobot`

If the older binary doesn't recognize a bucket created by the new binary, the bucket is simply ignored - no read/write touches it.

For destructive-schema rollbacks (rare), restore from a backup taken before the upgrade and stop the bot before swapping the file.
