#!/bin/bash
# calculate-cache-size.sh — Dynamically compute optimal kresd cache size
# Based on: min(50% free disk, 70% RAM, 8G)
#
# Usage:
#   source calculate-cache-size.sh
#   CACHE_SIZE=$(calculate_cache_size)
#
# Or directly:
#   ./calculate-cache-size.sh          # prints e.g. "2G"

calculate_cache_size() {
    # 1. Available disk space (for the root partition or PROJECT_DIR).
    # Default to this script's own directory so it works regardless of where
    # the project is cloned (and when invoked standalone, not via update.sh).
    local target_dir="${PROJECT_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)}"
    local disk_avail_mb
    disk_avail_mb=$(df -BM --output=avail "$target_dir" 2>/dev/null | tail -1 | tr -d ' M')

    # Reserve: keep at least 5GB free on disk after cache allocation
    local disk_reserve_mb=5120
    local disk_budget_mb=$(( disk_avail_mb - disk_reserve_mb ))
    local floored_by_disk=0
    if (( disk_budget_mb < 256 )); then
        disk_budget_mb=256
        floored_by_disk=1
    fi

    # 2. Available RAM — use 50% of total (leave room for ClickHouse, Prometheus, etc.)
    local total_mem_mb
    total_mem_mb=$(free -m 2>/dev/null | awk '/^Mem:/{print $2}' || echo "4096")
    local ram_budget_mb=$(( total_mem_mb * 50 / 100 ))

    # 3. Take the minimum of disk budget and RAM budget, cap at 8G
    local cache_mb=$disk_budget_mb
    if (( ram_budget_mb < cache_mb )); then
        cache_mb=$ram_budget_mb
    fi
    if (( cache_mb > 8192 )); then
        cache_mb=8192
    fi

    # 4. Floor at 256M
    if (( cache_mb < 256 )); then
        cache_mb=256
    fi

    # Shout when transient disk pressure — a docker build mid-update is enough —
    # has collapsed the cache to the floor. This result gets written into
    # config.yaml and STAYS there: the cache does not recover on its own, only
    # a later update run with roomier disk restores it. 216 silently ran a 256M
    # cache this way (13 Aug 2026). Pin CACHE_SIZE in .env to opt out entirely.
    if (( cache_mb <= 256 )) && (( floored_by_disk == 1 )); then
        echo "WARNING: cache floored at 256M — only ${disk_avail_mb}M disk free," \
             "under the ${disk_reserve_mb}M reserve. This value persists in" \
             "config/kresd/config.yaml until the next update. Free disk and re-run," \
             "or set a fixed CACHE_SIZE in .env." >&2
    fi

    # 5. Format: use GB if >= 1024MB, else MB
    if (( cache_mb >= 1024 )); then
        echo "$((cache_mb / 1024))G"
    else
        echo "${cache_mb}M"
    fi
}

# If executed directly (not sourced), print the result
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    calculate_cache_size
fi
