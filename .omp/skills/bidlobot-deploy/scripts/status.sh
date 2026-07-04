#!/usr/bin/env bash
# Quick status check for the bidlobot production deployment.
# Read-only — never mutates state.
set -uo pipefail

HOST="${BIDLOBOT_HOST:-veschin@192.168.0.101}"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
fail() { echo -e "${RED}FAIL:${NC} $*"; exit 1; }

echo -e "${GREEN}=== bidlobot status @ $(date -u +%Y-%m-%dT%H:%M:%SZ) ===${NC}"

ssh -o ConnectTimeout=5 -o BatchMode=yes "$HOST" 'bash -s' << 'ENDSSH'
set -euo pipefail
cd ~/bidlobot

# Container
echo -e "\n--- Container ---"
docker ps --filter name=bidlobot --format 'table {{.Names}}\t{{.Status}}\t{{.RunningFor}}' || echo "(no container)"

HEALTH=$(docker inspect bidlobot --format '{{.State.Health.Status}}' 2>/dev/null || echo "gone")
LAST_HEALTH=$(docker inspect bidlobot --format '{{.State.Health.Log | json}}' 2>/dev/null | python3 -c "
import sys,json
log=json.load(sys.stdin)
for e in log[-3:]:
    print(f'  [{e[\"Start\"][:19]}] exit={e[\"ExitCode\"]} -> {e[\"Output\"][:80].strip()}')
" 2>/dev/null || echo "(could not parse health log)")
echo "health: $HEALTH"
echo "$LAST_HEALTH"

# Git
echo -e "\n--- Git ---"
HEAD_SHORT=$(git rev-parse --short HEAD)
echo "HEAD: $HEAD_SHORT ($(git log -1 --format=%s))"
echo "ahead of origin: $(git rev-list --count origin/master..HEAD 2>/dev/null || echo '?')"

# Env (redacted)
echo -e "\n--- Env ---"
grep -E "^(CAPTCHA|TG_BOT|GLM|CLEANUP)_" env 2>/dev/null | sed 's/=.*/=REDACTED/' || echo "(no env file)"

# Recent errors
echo -e "\n--- Recent errors (last 5m) ---"
docker compose logs --since 5m bot 2>&1 | grep -i "ERROR\|WARN" | tail -5 || echo "(none)"

# Health endpoint
echo -e "\n--- Health endpoint ---"
HEALTH_BODY=$(docker exec bidlobot wget -qO- http://127.0.0.1:8080/health 2>/dev/null || echo '{"status":"unreachable"}')
echo "$HEALTH_BODY"

# Version
echo -e "\n--- Version ---"
docker exec bidlobot bidlobot --version 2>/dev/null || echo "(version check failed)"
ENDSSH
