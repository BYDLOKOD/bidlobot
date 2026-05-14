#!/bin/sh
# Host-side backup for the dockerized bot.
#
# Strategy: stop -> cp -> start. bbolt holds an exclusive flock while the
# bot is running, so the in-container `bidlobot-backup` binary cannot
# obtain even a read lock without the bot releasing it. A naive `cp` of a
# live mmap'd database can capture in-flight writes mid-transaction;
# bbolt's double-write meta + page checksums recovers from torn meta but
# not from torn data pages, so for a guaranteed-consistent snapshot the
# only safe path is to stop the bot first.
#
# Downtime is the duration of `cp` + `docker compose start` startup
# (~5-15s for a < 100 MB db on local disk). For a community moderation
# bot whose updates Telegram replays on next poll via offset, this is
# acceptable. If downtime becomes a constraint, run the bot behind a
# webhook with two replicas and switch the cron to bidlobot-backup
# inside one replica while the other handles traffic.
#
# Cron suggestion (root crontab on the deployment host):
#     17 3 * * * /opt/bidlobot/deploy/backup.sh >>/var/log/bidlobot-backup.log 2>&1

set -eu

CONTAINER="${BIDLOBOT_CONTAINER:-bidlobot}"
COMPOSE_DIR="${BIDLOBOT_COMPOSE_DIR:-/opt/bidlobot}"
BACKUP_DIR="${BIDLOBOT_BACKUP_DIR:-/var/backups/bidlobot}"
KEEP="${BIDLOBOT_BACKUP_KEEP:-7}"

mkdir -p "${BACKUP_DIR}"

if ! docker inspect -f '{{.State.Running}}' "${CONTAINER}" 2>/dev/null | grep -q true; then
    echo "$(date -u +%FT%TZ) container ${CONTAINER} not running, skipping (cron will alert via nonzero exit)" >&2
    exit 1
fi

STAMP="$(date -u +%Y%m%d-%H%M%S)"
DEST="${BACKUP_DIR}/bidlobot-${STAMP}.db"

echo "$(date -u +%FT%TZ) stopping ${CONTAINER} for consistent snapshot"
docker compose -f "${COMPOSE_DIR}/docker-compose.yml" stop bot

# bbolt source path resolves through the docker volume to the host disk.
# Use docker volume inspect so we don't hardcode /var/lib/docker.
VOL_PATH="$(docker volume inspect -f '{{.Mountpoint}}' bidlobot-data)"
SRC="${VOL_PATH}/bidlobot.db"

if [ ! -f "${SRC}" ]; then
    echo "$(date -u +%FT%TZ) ERROR: ${SRC} missing; restarting bot then aborting" >&2
    docker compose -f "${COMPOSE_DIR}/docker-compose.yml" start bot
    exit 2
fi

cp "${SRC}" "${DEST}.part"
mv "${DEST}.part" "${DEST}"

echo "$(date -u +%FT%TZ) restarting ${CONTAINER}"
docker compose -f "${COMPOSE_DIR}/docker-compose.yml" start bot

# Retain only the last $KEEP files by mtime.
find "${BACKUP_DIR}" -maxdepth 1 -name 'bidlobot-*.db' -type f -printf '%T@ %p\n' \
    | sort -n \
    | head -n "-${KEEP}" \
    | awk '{ for (i = 2; i <= NF; i++) printf "%s%s", $i, (i==NF?"\n":" ") }' \
    | xargs -r rm -f

echo "$(date -u +%FT%TZ) backup complete: ${DEST}"
