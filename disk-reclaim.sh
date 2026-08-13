#!/bin/bash
# disk-reclaim.sh — Automatic disk reclaim for knot-dns-monitor
# Run via cron: 0 */6 * * * /path/to/disk-reclaim.sh >> /var/log/disk-reclaim.log 2>&1

set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$PROJECT_DIR"

DISK_WARN_PERCENT=85
DISK_EARLY_WARN_PERCENT=75
CH_RETENTION_DAYS=14
LOG_PREFIX="[disk-reclaim $(date '+%Y-%m-%d %H:%M:%S')]"

log() { echo "$LOG_PREFIX $*"; }

disk_usage_percent() {
    df / --output=pcent | tail -1 | tr -d ' %'
}

log "=== Starting disk reclaim ==="
log "Disk usage before: $(disk_usage_percent)%"

# 1. ClickHouse: force TTL cleanup and drop old partitions beyond retention
log "Cleaning ClickHouse data older than ${CH_RETENTION_DAYS} days..."
CUTOFF_DATE=$(date -d "-${CH_RETENTION_DAYS} days" '+%Y-%m-%d')
# Get partitions older than cutoff
OLD_PARTITIONS=$(docker compose exec -T clickhouse clickhouse-client -d dnsmonitor \
    --query "SELECT DISTINCT partition FROM system.parts WHERE database='dnsmonitor' AND table='dns_queries' AND active AND partition < '${CUTOFF_DATE}' FORMAT TabSeparated" 2>/dev/null || true)

if [ -n "$OLD_PARTITIONS" ]; then
    while IFS= read -r part; do
        [ -z "$part" ] && continue
        log "  Dropping partition: $part"
        docker compose exec -T clickhouse clickhouse-client -d dnsmonitor \
            --query "ALTER TABLE dns_queries DROP PARTITION '${part}';" 2>/dev/null || true
    done <<< "$OLD_PARTITIONS"
fi

# Also clean aggregation tables
for tbl in dns_queries_1m top_domains_1h; do
    docker compose exec -T clickhouse clickhouse-client -d dnsmonitor \
        --query "ALTER TABLE ${tbl} DELETE WHERE toDate(timestamp) < today() - 60;" 2>/dev/null || true
done

# Optimize tables to reclaim disk
docker compose exec -T clickhouse clickhouse-client -d dnsmonitor \
    --query "OPTIMIZE TABLE dns_queries FINAL;" 2>/dev/null || true
docker compose exec -T clickhouse clickhouse-client -d dnsmonitor \
    --query "OPTIMIZE TABLE dns_queries_1m FINAL;" 2>/dev/null || true
docker compose exec -T clickhouse clickhouse-client -d dnsmonitor \
    --query "OPTIMIZE TABLE top_domains_1h FINAL;" 2>/dev/null || true

log "ClickHouse cleanup done"

# 1b. ClickHouse system tables — text_log/query_log/etc default ke unlimited.
# system_logs.xml in config.d normally caps these, but apply TTL idempotently here too
# so existing deployments without the XML still get bounded growth.
log "Applying TTL to ClickHouse system tables..."
declare -A SYSTEM_TTL=(
    [text_log]=3
    [trace_log]=3
    [metric_log]=3
    [asynchronous_metric_log]=3
    [processors_profile_log]=3
    [part_log]=7
    [query_log]=7
    [query_views_log]=7
)
for tbl in "${!SYSTEM_TTL[@]}"; do
    days="${SYSTEM_TTL[$tbl]}"
    docker compose exec -T clickhouse clickhouse-client \
        --query "ALTER TABLE system.${tbl} MODIFY TTL event_date + INTERVAL ${days} DAY DELETE;" 2>/dev/null || true
done
# OPTIMIZE FINAL forces TTL to materialize (lazy by default)
for tbl in text_log trace_log metric_log asynchronous_metric_log processors_profile_log part_log query_log query_views_log; do
    docker compose exec -T clickhouse clickhouse-client \
        --query "OPTIMIZE TABLE system.${tbl} FINAL;" 2>/dev/null || true
done
log "ClickHouse system tables TTL applied"

# 2. Truncate old container logs — runaway crash-loop containers (e.g. postgres "no space
# left") balloon json.log fast; truncate at >10M (was 50M, too lax for crash-loop pace).
log "Truncating large container logs..."
find /var/lib/docker/containers/ -name "*-json.log" -size +10M -exec truncate -s 0 {} \; 2>/dev/null || true

# 3. Docker system prune (unused images, build cache, dead containers)
log "Pruning Docker system..."
docker system prune -f --volumes 2>/dev/null | tail -1 || true
docker builder prune -f 2>/dev/null | tail -1 || true

# 4. Clean old journal logs (keep 3 days)
if command -v journalctl &>/dev/null; then
    log "Vacuuming journal logs..."
    journalctl --vacuum-time=3d --quiet 2>/dev/null || true
fi

# 5. Clean apt cache
if command -v apt-get &>/dev/null; then
    apt-get clean -qq 2>/dev/null || true
fi

USAGE_AFTER=$(disk_usage_percent)
log "Disk usage after: ${USAGE_AFTER}%"

# 6a. Early warning: disk above 75% — proactive alert
if [ "$USAGE_AFTER" -ge "$DISK_EARLY_WARN_PERCENT" ] && [ "$USAGE_AFTER" -lt "$DISK_WARN_PERCENT" ]; then
    log "EARLY WARNING: Disk at ${USAGE_AFTER}% — above ${DISK_EARLY_WARN_PERCENT}% threshold. Consider manual cleanup or increasing retention monitoring."
fi

# 6b. Emergency: if still above threshold, reduce ClickHouse to 7 days
if [ "$USAGE_AFTER" -ge "$DISK_WARN_PERCENT" ]; then
    log "WARNING: Disk still at ${USAGE_AFTER}% — emergency cleanup, reducing to 7 days retention"
    EMERGENCY_DATE=$(date -d "-7 days" '+%Y-%m-%d')
    EMERGENCY_PARTS=$(docker compose exec -T clickhouse clickhouse-client -d dnsmonitor \
        --query "SELECT DISTINCT partition FROM system.parts WHERE database='dnsmonitor' AND table='dns_queries' AND active AND partition < '${EMERGENCY_DATE}' FORMAT TabSeparated" 2>/dev/null || true)
    if [ -n "$EMERGENCY_PARTS" ]; then
        while IFS= read -r part; do
            [ -z "$part" ] && continue
            log "  Emergency drop partition: $part"
            docker compose exec -T clickhouse clickhouse-client -d dnsmonitor \
                --query "ALTER TABLE dns_queries DROP PARTITION '${part}';" 2>/dev/null || true
        done <<< "$EMERGENCY_PARTS"
        docker compose exec -T clickhouse clickhouse-client -d dnsmonitor \
            --query "OPTIMIZE TABLE dns_queries FINAL;" 2>/dev/null || true
    fi
    log "Disk usage final: $(disk_usage_percent)%"
fi

# 7. Adjust kresd cache size if it changed
#
# This block restarts kresd, so it is the single most dangerous thing this
# cron does. It caused repeated fleet outages on 12-13 Aug 2026 and has been
# tightened on four counts — do not loosen any of them casually:
#
#   1. A pinned CACHE_SIZE is honoured. Recalculating from free disk every
#      night made the cache flap (216 went 256M -> 7G at 03:00, then back to
#      256M at 04:25 when an update ran while a docker build held the disk).
#      Every flap costs a kresd restart.
#   2. Small changes are ignored. Without hysteresis, ordinary disk noise was
#      enough to trigger a restart.
#   3. --no-deps, so bringing kresd up cannot drag the rest of the stack with it.
#   4. dnsdist is restarted AFTER kresd. It resolves the kresd backend IP once
#      at startup, so a recreated kresd leaves it forwarding to a dead address:
#      port 53 goes dark while every health check stays green. This step was
#      missing entirely, which is how the resolver kept dying overnight.
PINNED_CACHE=""
if [ -f "${PROJECT_DIR}/.env" ]; then
    PINNED_CACHE=$(grep -E '^CACHE_SIZE=' "${PROJECT_DIR}/.env" 2>/dev/null | cut -d= -f2- | tr -d ' ')
fi

cache_to_mb() {
    case "$1" in
        *G|*g) echo $(( ${1%[Gg]} * 1024 )) ;;
        *M|*m) echo "${1%[Mm]}" ;;
        *)     echo 0 ;;
    esac
}

if [ -n "$PINNED_CACHE" ] && [ "$PINNED_CACHE" != "auto" ]; then
    log "kresd cache pinned to ${PINNED_CACHE} in .env — skipping automatic resize"
elif [ -f "${PROJECT_DIR}/calculate-cache-size.sh" ]; then
    # shellcheck source=./calculate-cache-size.sh
    source "${PROJECT_DIR}/calculate-cache-size.sh"
    NEW_CACHE=$(calculate_cache_size)
    CURRENT_CACHE=$(grep 'size-max:' "${PROJECT_DIR}/config/kresd/config.yaml" 2>/dev/null | awk '{print $2}' || echo "")

    NEW_MB=$(cache_to_mb "$NEW_CACHE")
    CUR_MB=$(cache_to_mb "$CURRENT_CACHE")

    # Restart only for a change worth the downtime: at least 512M of movement
    # AND at least 25% of the current size.
    DELTA=$(( NEW_MB > CUR_MB ? NEW_MB - CUR_MB : CUR_MB - NEW_MB ))
    THRESHOLD=$(( CUR_MB / 4 ))
    [ "$THRESHOLD" -lt 512 ] && THRESHOLD=512

    if [ -n "$NEW_CACHE" ] && [ "$NEW_CACHE" != "$CURRENT_CACHE" ] && [ "$NEW_MB" -gt 0 ] && [ "$DELTA" -ge "$THRESHOLD" ]; then
        log "Adjusting kresd cache: ${CURRENT_CACHE} -> ${NEW_CACHE} (delta ${DELTA}M >= ${THRESHOLD}M)"
        sed -i "s/size-max: .*/size-max: ${NEW_CACHE}/" "${PROJECT_DIR}/config/kresd/config.yaml"
        # Restart kresd to apply new cache size
        docker compose stop kresd dnstap-ingester 2>/dev/null || true
        docker compose up -d --no-deps dnstap-ingester 2>/dev/null || true
        sleep 2
        docker compose up -d --no-deps kresd 2>/dev/null || true
        sleep 2
        # MUST come after kresd: re-resolve the backend IP (see note above).
        docker compose restart dnsdist 2>/dev/null || true
        log "kresd restarted with cache ${NEW_CACHE}; dnsdist re-resolved"
    elif [ -n "$NEW_CACHE" ] && [ "$NEW_CACHE" != "$CURRENT_CACHE" ]; then
        log "kresd cache ${CURRENT_CACHE} -> ${NEW_CACHE} below hysteresis (delta ${DELTA}M < ${THRESHOLD}M) — not restarting"
    fi
fi

log "=== Disk reclaim complete ==="
