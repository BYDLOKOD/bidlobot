#!/bin/sh
# scripts/backup.sh - rotate-friendly bbolt backup wrapper.
#
# Strategy
# --------
# bbolt does not support cross-process online backup: the running bot
# holds an exclusive flock on data/bidlobot.db, so a sibling process
# opening the file ReadOnly would block waiting for that lock.
#
# Two execution paths:
#
#   1. Bot is NOT running. Either it is stopped for maintenance or the
#      backup is being taken on a snapshot of /var/lib. In this case the
#      `bidlobot-backup` Go binary (cmd/bidlobot-backup) opens the DB
#      ReadOnly and uses bolt.Tx.WriteTo for a guaranteed-consistent
#      snapshot.
#
#   2. Bot is running. We fall back to a plain `cp` of the file. bbolt's
#      meta pages are double-written + checksummed, so a torn meta is
#      recovered on next open, but in-progress write transactions may
#      copy partially-mutated data pages. For a moderation bot whose
#      writes are stats counters and warning records, the worst case is
#      losing a few seconds of activity at restore time - acceptable for
#      "best-effort daily backup" semantics. For a true point-in-time
#      backup, stop the bot first.
#
# Concurrency: flock(1) on /tmp/bidlobot-backup.lock prevents two
# overlapping backup runs (cron + manual). -n means "fail fast" if the
# lock is held; otherwise we'd queue and risk thundering-herd at the
# scheduled hour.
#
# Output: backups/bidlobot-YYYYMMDD-HHMMSS.db
# Rotation: keep last 7 by mtime, delete older.

set -eu

DB_PATH="${DB_PATH:-data/bidlobot.db}"
BACKUP_DIR="${BACKUP_DIR:-backups}"
KEEP="${BACKUP_KEEP:-7}"
LOCK_FILE="${BACKUP_LOCK:-/tmp/bidlobot-backup.lock}"
BACKUP_BIN="${BACKUP_BIN:-bidlobot-backup}"

log() { printf '[%s] %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }
die() { log "ERROR: $*" >&2; exit 1; }

acquire_lock() {
    if ! command -v flock >/dev/null 2>&1; then
        log "WARN: flock(1) not available; backup runs without overlap protection"
        return 0
    fi
    # Acquire on fd 9; held until script exit.
    exec 9>"$LOCK_FILE" || die "cannot open lock file $LOCK_FILE"
    if ! flock -n 9; then
        die "another backup is in progress (held lock $LOCK_FILE)"
    fi
}

ensure_backup_dir() {
    mkdir -p "$BACKUP_DIR" || die "cannot create $BACKUP_DIR"
}

write_snapshot() {
    src="$1"
    dst="$2"

    # Try the Go binary first. It returns non-zero quickly if the DB is
    # locked exclusive (bot is running), so we can fall through.
    if command -v "$BACKUP_BIN" >/dev/null 2>&1; then
        log "trying online backup via $BACKUP_BIN"
        if "$BACKUP_BIN" -src "$src" -out "$dst" -timeout 1s; then
            return 0
        fi
        log "WARN: $BACKUP_BIN failed (likely bot is running); falling back to cp"
    else
        log "INFO: $BACKUP_BIN not found in PATH; using cp directly"
    fi

    # Fallback: file copy. Atomic via rename so consumers never see a
    # partial backup file.
    tmp="${dst}.part"
    cp -- "$src" "$tmp" || die "cp $src $tmp failed"
    sync
    mv -- "$tmp" "$dst" || die "rename to $dst failed"
}

rotate() {
    keep="$1"
    # ls -t orders by mtime newest first; tail skips the first $keep.
    # Using find for robustness against spaces in the dir path.
    files=$(find "$BACKUP_DIR" -maxdepth 1 -type f -name 'bidlobot-*.db' -printf '%T@ %p\n' 2>/dev/null \
            | sort -rn \
            | awk '{ print $2 }')
    n=0
    echo "$files" | while IFS= read -r f; do
        [ -z "$f" ] && continue
        n=$((n + 1))
        if [ "$n" -gt "$keep" ]; then
            rm -f -- "$f" || log "WARN: rotation could not remove $f"
        fi
    done
}

main() {
    [ -f "$DB_PATH" ] || die "source db not found: $DB_PATH"

    acquire_lock
    ensure_backup_dir

    ts=$(date -u +%Y%m%d-%H%M%S)
    dst="$BACKUP_DIR/bidlobot-${ts}.db"

    log "backing up $DB_PATH -> $dst"
    write_snapshot "$DB_PATH" "$dst"
    log "rotating backups (keep=$KEEP)"
    rotate "$KEEP"

    bytes=$(wc -c < "$dst" | tr -d ' ')
    log "OK $dst ($bytes bytes)"
}

main "$@"
