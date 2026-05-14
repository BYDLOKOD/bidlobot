---
id: devlog-01
kind: log
---

# Devlog #01: Dockerization, critic-driven hardening, public release, prod deploy

Date: 2026-05-14 / 2026-05-15
Commits: a2a7e5e..HEAD (this session adds the deploy bundle + hardening)
Duration: ~3h

## Context

After the 22-commit feature push (Phases 0-5: scope pivot, membership,
inline, pending+callback dispatcher, mini-games, production tgclient
wrapper, /health, graceful shutdown), the bot needed to actually run
24/7 in a real chat. The user requested: "подготовь бота к деплою.
пускай независимый критик проверит его, ... распланируй полноценный
докер образ. залей все что нужно в апстрим. и задеплой на серваке".

## Timeline

### 2026-05-14 23:30 - inventory

`git remote -v` empty -- no upstream existed. `~/.ssh/config` only
gitlab.akb-it.ru. No Dockerfile, no docker-compose.yml, no SSH alias
for any prod target. `.github/workflows/ci.yml` already had vet +
test -race + build. `gh auth status` -> `veschin` on github.com (SSH
protocol).

Asked the user: where to push, where to deploy, which token. Answers:
- public github.com/veschin/bidlobot
- <deploy-host> (`ssh veschin@<deploy-host>`)
- same token from `.env`

### 23:35 - server probe

`ping <deploy-host>` -> reachable on the local LAN. SSH key auth
worked without password (id_rsa already in authorized_keys). Server:
Ubuntu 24.04+, Docker 27.0.3, Compose v2.40.3, 27 GB RAM, 430 GB free.
Ports 80/443/8080/8081 already occupied by other services. Conclusion:
the bot's healthcheck stays inside the compose network, no host port
publish.

### 23:50 - first Docker stack

Multi-stage Dockerfile (golang:1.26-alpine -> alpine:3.20), CGO_ENABLED=0
(every dep is pure Go), tini PID 1, non-root UID 65532. compose YAML
single replica (Telegram allows only one getUpdates poller per
token), `restart: unless-stopped`, `stop_grace_period: 15s`,
healthcheck via wget against the loopback. Image: 30.3 MB. Smoke
tests against the image:

- `--version` prints the build banner with commit + go version
- `--check-config` exit=1 with empty token, exit=0 with a valid-shape
  one
- All `go test -race ./...` packages green

### 00:05 - critic invocation

Triggered the critic with model=opus, ten scenarios spanning token
leak, container root, healthcheck, SIGTERM, single-instance, backup,
build hygiene, CI integrity, operator footguns, and public-repo blast
radius. Found three critical issues + several major ones:

1. `cmd/demo/main.go` hardcoded a real Telegram chat id (-1009000002)
   and user id -- not a secret per se, but identifies the test chat
   owner once the repo goes public.
2. `deploy/backup.sh` passed `-dst` but `cmd/bidlobot-backup`'s flag
   is `-out` -- every cron run would `os.Exit(2)`. Even with the flag
   fixed, the backup binary's read-only bbolt open contends with the
   bot's exclusive write lock and times out. The script as written
   would never produce a working backup.
3. Volume permission bug: empty named volume defaults to root:root
   0755, so the unprivileged bot user fails on first bbolt.Open.
4. `dbOpen` in main.go was a tautology (`db != nil && db.Path() != ""`)
   -- `db.Path()` never clears on Close, so the check never flipped.
5. `cmd/smoke` + same token as prod = silent split-brain via 409
   Conflict on getUpdates. Half the user updates are lost.

### 00:30 - fix pass

Choices made:

- **Removed `cmd/demo` and `cmd/smoke` entirely.** They had served
  their purpose (in-chat demo + production-wired bounded smoke).
  Going forward, `docker compose logs -f bot` is the same signal.
  Removing them closes both the hardcoded-id leak and the
  duplicate-token race vector.
- **Rewrote `deploy/backup.sh` as host-side `stop -> cp -> start`.**
  Trades ~10 s downtime for a guaranteed-consistent snapshot. Resolves
  the bbolt mount path via `docker volume inspect`, exits nonzero on
  any failure so cron alerts.
- **Added a `.keep` marker** to `/var/lib/bidlobot/` in the image so
  Docker copies image-baked ownership into a fresh named volume.
- **Replaced `dbOpen`** with a no-op `db.View` transaction.
- **Cached `getMe` in /health** with a 60 s TTL so brief Telegram 5xx
  bursts don't trigger compose restart loops during the very incident
  the bot is supposed to ride out.
- **Bumped `start_period: 20s -> 60s`** and `stop_grace_period: 15s
  -> 30s`.
- **Tightened `.gitignore`** (`*.env`, `deploy/env`, `backups/`).
- **Added `.dockerignore` rules** for `scripts/` (host-only).
- **Added two CI jobs**: docker buildx build (no push) + gitleaks.

### 00:55 - re-verification

`go test -race ./...`: green across 14 packages. `docker build` after
fixes: 30.3 MB unchanged. `--version`/`--check-config` smoke: exit
codes match expectations.

### 01:00 - push and deploy

(Pending in this same session.)

## Outcome

Deploy bundle: `Dockerfile`, `docker-compose.yml`, `.dockerignore`,
`deploy/{env.example,backup.sh}`, hardened `cmd/bidlobot/main.go`,
`internal/bot/health.go`, `.gitignore`, `.github/workflows/ci.yml`.
Removed: `cmd/demo`, `cmd/smoke`. Image: 30.3 MB, alpine + tini +
three binaries (bidlobot, bidlobot-backup, bidlobot-probe), runs as
UID 65532, healthcheck internal-only.

Tests: 14 packages green with `-race`. Docker image builds clean.
Critic's 3 critical + 4 major issues all addressed. Remaining minor
issues (action SHA pinning, `--check-config` preflight container)
deferred to next iteration.

## Artifacts

- (none captured visually this session)

## Seeds

- "Three critic findings I'd missed and one I had rationalized away"
  -- the volume-ownership bug and the `dbOpen` tautology were both
  things I'd half-noticed but moved past; the demo-chat leak and the
  backup-flag bug were genuine misses.
- "Stop-cp-start vs hot snapshot: when 10 seconds of downtime is
  cheaper than the snapshot machinery."
- "Removing test infrastructure as the production hardening: cmd/smoke
  was a duplicate-token weapon pointing at prod from the dev's own
  shell."
