#!/usr/bin/env bash
# Deploy bidlobot to the production host.
# Usage: ./deploy.sh [--skip-push]
#   --skip-push  assume the latest commit is already on origin/master
set -uo pipefail

HOST="${BIDLOBOT_HOST:-veschin@192.168.0.101}"
REPO_DIR="${BIDLOBOT_REPO_DIR:-/home/veschin/bidlobot}"
LOCAL_REPO="${BIDLOBOT_LOCAL_REPO:-.}"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
fail() { echo -e "${RED}FAIL:${NC} $*"; exit 1; }
info() { echo -e "${GREEN}->${NC} $*"; }

SKIP_PUSH=false
[[ "${1:-}" == "--skip-push" ]] && SKIP_PUSH=true

# --- Pre-flight ---
info "checking SSH to $HOST"
ssh -o ConnectTimeout=5 -o BatchMode=yes "$HOST" 'hostname' >/dev/null 2>&1 \
  || fail "cannot SSH to $HOST (is the VM running? try: ssh $HOST 'hostname')"

# --- Push (unless skipped) ---
if ! $SKIP_PUSH; then
  info "checking local repo"
  cd "$LOCAL_REPO"
  git rev-parse --is-inside-work-tree >/dev/null 2>&1 || fail "$LOCAL_REPO is not a git repo"

  LOCAL_SHA=$(git rev-parse HEAD)
  REMOTE_SHA=$(git ls-remote origin HEAD | awk '{print $1}')
  if [[ "$LOCAL_SHA" != "$REMOTE_SHA" ]]; then
    info "pushing $LOCAL_SHA -> origin/master"
    git push origin master || fail "git push failed"
  else
    info "local HEAD already on origin, skipping push"
  fi
fi

# --- Deploy ---
info "deploying to $HOST"
ssh "$HOST" "bash -s" << 'ENDSSH'
set -euo pipefail
cd ~/bidlobot

echo "-> pulling latest"
git fetch origin
git reset --hard origin/master
HEAD_SHORT=$(git rev-parse --short HEAD)
echo "-> HEAD at $HEAD_SHORT"

echo "-> building + restarting"
docker compose up -d --build 2>&1 | grep -E "Built|Recreat|Started|Error|error" || true

echo "-> waiting for healthy (max 30s)"
for i in $(seq 1 15); do
  STATUS=$(docker inspect bidlobot --format '{{.State.Health.Status}}' 2>/dev/null || echo "gone")
  if [[ "$STATUS" == "healthy" ]]; then
    echo "-> healthy after $((i*2))s"
    break
  fi
  sleep 2
done

if [[ "${STATUS:-}" != "healthy" ]]; then
  echo "WARNING: container status = ${STATUS:-unknown}"
fi

echo "-> recent logs"
docker compose logs --since 30s bot 2>&1 | grep -E "captcha|starting|bot started|ERROR|WARN" | tail -10 || true
ENDSSH

info "deploy complete - verify with: scripts/status.sh"
