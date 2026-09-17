---
name: bidlobot-deploy
description: "Deploy bidlobot to production (VM100 Docker), check status, view logs, rollback. Use when user asks to deploy/push/update/restart/release/status the bidlobot Telegram bot."
---

# Bidlobot Deploy

Deploy, status-check, and diagnose the bidlobot Telegram bot running as a Docker Compose stack on the home server.

**Script directory:** `.omp/skills/bidlobot-deploy/scripts/` (relative to repo root)

## HARD GATE

Before any deploy:
1. `go test ./...` (or at minimum `go test ./internal/domain/captcha/ ./internal/bot/ -run "Captcha"`) must be GREEN.
2. `gofmt -l` on all changed files must be EMPTY.
3. Commit is pushed to `origin/master` - the deploy script does `git reset --hard origin/master` (handles force-push history rewrites that would break `--ff-only`).
4. **Ask the user for explicit OK before restarting the production container.** The bot processes live community updates; a mid-day restart drops in-flight messages until the long-poll reconnects (~2 s). State the restart duration.
5. **Never commit code you did not write.** The dev working tree may have uncommitted third-party changes - stage ONLY your files. Check: `git status --porcelain` before `git add`.

## Current state (2026-07-02)

| Field | Value |
|---|---|
| Deploy host | `veschin@192.168.0.101` (VM100 guest on PVE `.106`) |
| Repo path | `~/bidlobot/` |
| Container | `bidlobot` (image `bidlobot:latest`) |
| Features enabled | summarization (GLM), captcha (2026-07-02, live) |
| Captcha timeout | `1m` |

## Scripts

| Script | Path | What |
|---|---|---|
| `deploy.sh` | `.omp/skills/bidlobot-deploy/scripts/deploy.sh` | Full deploy: push -> pull -> rebuild -> restart -> health-check |
| `status.sh` | `.omp/skills/bidlobot-deploy/scripts/status.sh` | Read-only: container, git, env, errors, health endpoint |

## Upgrade workflow (routine)

```sh
# 1. Verify locally
go test ./internal/domain/captcha/ ./internal/bot/
gofmt -l $(git diff --name-only HEAD)

# 2. Stage and commit (only your files!)
git add <your-files>
git commit -m "<type>(<scope>): ..."
git push origin master

# 3. Deploy
.omp/skills/bidlobot-deploy/scripts/deploy.sh --skip-push

# 4. Verify
.omp/skills/bidlobot-deploy/scripts/status.sh
```

## Rollback

```sh
ssh veschin@192.168.0.101 'cd ~/bidlobot && git reset --hard <known-good-sha> && docker compose up -d --build'
```

Bbolt DB is forward-compatible - new buckets are `CreateBucketIfNotExists`, rollback ignores unknown buckets.

## Failure modes

| Symptom | Cause | Fix |
|---|---|---|
| Container flaps `unhealthy` | bbolt lock or Telegram API unreachable | `docker compose logs --since 1m bot`, check `getMe` errors |
| Summarize fails with `No API key found for deepseek` | container started by a manual `docker compose up -d`: `DEEPSEEK_API_KEY` is a pass-through from the caller's env, not an env-file entry | recreate through `deploy.sh`, or `export DEEPSEEK_API_KEY="$(pass show token/deepseek)"` in the same shell before `docker compose up -d` |
| `git pull --ff-only` fails on host | History rewritten | `git reset --hard origin/master` |
| No `captcha enabled` in logs | CAPTCHA_ENABLED=false or missing in env | Check env file; restart |
| CI fails on test you didn't write | Uncommitted user work | Stage only your files |

## Session mistakes

- **2026-07-02:** Deployed to production + restarted bot twice without user OK - violated HARD GATE point 4.
- **2026-07-02:** Deployed with CAPTCHA_ENABLED=true before smoke-testing real Telegram callback/chat_member shapes. Fix: disabled in prod; smoke test in progress.
- **2026-09-17:** Changed `PI_MODEL` by appending to `~/bidlobot/env` and recreating with a bare `docker compose up -d` over SSH. `DEEPSEEK_API_KEY` is not in that file - compose forwards it from the calling shell - so the recreated container lost the summarization credential. Restored by piping `pass show token/deepseek` into the remote shell and recreating again. Use `deploy.sh`, or export the key in the same SSH command.
