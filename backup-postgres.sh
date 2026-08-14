#!/bin/bash
# backup-postgres.sh — Daily PostgreSQL backup for knot-dns-monitor
#
# PostgreSQL is the only store here that CANNOT be regenerated: users,
# cluster pairing tokens, RPZ/alert/notify configuration. ClickHouse and
# Prometheus refill themselves with time; this does not. The whole database
# is ~8 MB, so a daily dump costs nothing and buys everything back.
#
# Run via cron (installed idempotently by update.sh):
#   15 4 * * * /root/knot-dns-monitor/backup-postgres.sh >> /var/log/pg-backup.log 2>&1
#
# Restore:
#   gunzip -c /root/backups/pg/pg-dnsmonitor-<stamp>.sql.gz | \
#     docker exec -i knot-dns-monitor-postgres-1 psql -U dnsmon -d dnsmonitor

set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BACKUP_DIR="${BACKUP_DIR:-/root/backups/pg}"
KEEP_DAYS="${KEEP_DAYS:-14}"
STAMP="$(date +%Y%m%d-%H%M%S)"
OUT="${BACKUP_DIR}/pg-dnsmonitor-${STAMP}.sql.gz"

log() { echo "[pg-backup $(date '+%Y-%m-%d %H:%M:%S')] $*"; }

mkdir -p "$BACKUP_DIR"

# Find the postgres container regardless of compose project name.
CONTAINER=$(docker ps --filter "label=com.docker.compose.service=postgres" --format '{{.Names}}' | head -1)
if [ -z "$CONTAINER" ]; then
    log "ERROR: postgres container not found"
    exit 1
fi

log "Dumping dnsmonitor from ${CONTAINER}..."
if ! docker exec "$CONTAINER" pg_dump -U dnsmon --clean --if-exists dnsmonitor | gzip > "$OUT"; then
    rm -f "$OUT"
    log "ERROR: pg_dump failed"
    exit 1
fi

# A dump that gzips to under a kilobyte is not a backup — refuse to count it.
SIZE=$(stat -c %s "$OUT")
if [ "$SIZE" -lt 1024 ]; then
    log "ERROR: dump suspiciously small (${SIZE} bytes) — keeping previous backups, marking failure"
    mv "$OUT" "${OUT}.suspect"
    exit 1
fi

log "OK: ${OUT} ($(numfmt --to=iec "$SIZE" 2>/dev/null || echo "${SIZE}B"))"

# Rotate
DELETED=$(find "$BACKUP_DIR" -name 'pg-dnsmonitor-*.sql.gz' -mtime +"$KEEP_DAYS" -print -delete | wc -l)
[ "$DELETED" -gt 0 ] && log "Rotated out ${DELETED} backup(s) older than ${KEEP_DAYS} days"

log "Done. $(ls "$BACKUP_DIR" | wc -l) backup(s) on disk."
