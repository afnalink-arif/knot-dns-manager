#!/bin/bash
set -euo pipefail

# ============================================
# KResD Manager - Updater
# ============================================
# Pulls latest code, rebuilds custom images,
# and restarts services with zero-downtime DNS.
# ============================================

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

NONINTERACTIVE="${NONINTERACTIVE:-0}"
PROJECT_DIR="$(cd "$(dirname "$0")" && pwd)"

if [[ "$NONINTERACTIVE" == "1" ]]; then
  info()  { echo "[INFO] $1"; }
  ok()    { echo "[OK] $1"; }
  warn()  { echo "[WARN] $1"; }
  error() { echo "[ERROR] $1"; exit 1; }
else
  info()  { echo -e "${BLUE}[INFO]${NC} $1"; }
  ok()    { echo -e "${GREEN}[OK]${NC} $1"; }
  warn()  { echo -e "${YELLOW}[WARN]${NC} $1"; }
  error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }
fi

# When running inside a container, docker compose can't resolve relative bind
# mount paths (daemon sees container paths, not host paths).
# Re-execute this script in a new container with the correct host mount.
if [[ -f /.dockerenv ]] && command -v docker &>/dev/null; then
    HOST_PATH=$(docker inspect "$(hostname)" --format '{{range .Mounts}}{{if eq .Destination "/project"}}{{.Source}}{{end}}{{end}}' 2>/dev/null || echo "")
    if [[ -n "$HOST_PATH" ]]; then
        BACKEND_IMAGE=$(docker inspect "$(hostname)" --format '{{.Config.Image}}' 2>/dev/null || echo "")
        if [[ -z "$BACKEND_IMAGE" ]]; then
            error "Cannot detect backend image"
        fi
        info "Delegating to host path: ${HOST_PATH}"
        exec docker run --rm \
            -v /var/run/docker.sock:/var/run/docker.sock \
            -v "${HOST_PATH}:${HOST_PATH}:rw" \
            -w "${HOST_PATH}" \
            -e NONINTERACTIVE=1 \
            -e TERM=dumb \
            --network host \
            --entrypoint bash "$BACKEND_IMAGE" "${HOST_PATH}/update.sh"
    fi
fi

echo ""
echo -e "${BLUE}╔══════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║   KResD Manager - Updater             ║${NC}"
echo -e "${BLUE}╚══════════════════════════════════════════╝${NC}"
echo ""

cd "$PROJECT_DIR"

# ---- Pre-flight checks ----
if [[ ! -f .env ]]; then
    error ".env not found. Run install.sh first."
fi

if [[ ! -f docker-compose.yml ]]; then
    error "docker-compose.yml not found. Are you in the project directory?"
fi

# Load current config
source .env

# ---- Step 1: Pull latest code ----
info "Pulling latest code from git..."
if git rev-parse --is-inside-work-tree &>/dev/null; then
    BEFORE=$(git rev-parse HEAD)
    git pull --ff-only 2>&1 || {
        warn "git pull failed. You may have local changes."
        if [[ "$NONINTERACTIVE" == "1" ]]; then
            warn "Non-interactive mode, continuing anyway."
        else
            read -rp "  Continue anyway? [y/N]: " CONT
            [[ "$CONT" =~ ^[Yy]$ ]] || exit 1
        fi
    }
    AFTER=$(git rev-parse HEAD)

    if [[ "$BEFORE" == "$AFTER" ]]; then
        info "Already up to date."
        if [[ "$NONINTERACTIVE" == "1" ]]; then
            info "Non-interactive mode, rebuilding anyway."
        else
            read -rp "Force rebuild anyway? [y/N]: " FORCE
            if [[ ! "$FORCE" =~ ^[Yy]$ ]]; then
                echo "Nothing to do."
                exit 0
            fi
        fi
    else
        COMMITS=$(git log --oneline "${BEFORE}..${AFTER}" | wc -l)
        ok "Pulled ${COMMITS} new commit(s)"
        echo ""
        git log --oneline "${BEFORE}..${AFTER}" | head -10
        echo ""
    fi
else
    warn "Not a git repository, skipping pull"
fi

# ---- Step 2: Regenerate configs from templates (if templates changed) ----
info "Regenerating configs from templates..."

if [[ -f config/Caddyfile.template ]]; then
    sed "s/__DOMAIN__/${DOMAIN}/g" config/Caddyfile.template > config/Caddyfile
    ok "Regenerated: config/Caddyfile"
fi

if [[ -f config/kresd/config.yaml.template ]]; then
    # Recalculate cache size dynamically based on current disk/RAM
    # Always recalculate unless CACHE_SIZE is explicitly set to a fixed value
    if [[ -z "$CACHE_SIZE" || "$CACHE_SIZE" == "auto" ]] && [[ -f "$(dirname "$0")/calculate-cache-size.sh" ]]; then
        source "$(dirname "$0")/calculate-cache-size.sh"
        CACHE_SIZE=$(calculate_cache_size)
        ok "Dynamic cache size: ${CACHE_SIZE}"
    else
        ok "Fixed cache size: ${CACHE_SIZE}"
    fi

    SUBNET_VIEWS=""
    IFS=',' read -ra SUBNET_ARRAY <<< "$ALLOWED_SUBNETS"
    for subnet in "${SUBNET_ARRAY[@]}"; do
        subnet=$(echo "$subnet" | xargs)
        SUBNET_VIEWS="${SUBNET_VIEWS}  - subnets: ['${subnet}']
    answer: allow
"
    done

    # Preserve existing local-data (filter blocklist) from current config.
    # awk captures `local-data:` and every following indented line until the
    # next top-level key or EOF — handles the block being last in the file.
    LOCAL_DATA=""
    if [[ -f config/kresd/config.yaml ]]; then
        LOCAL_DATA=$(awk '
            /^local-data:/ { capture=1; print; next }
            capture && /^[^ ]/ { capture=0 }
            capture { print }
        ' config/kresd/config.yaml)
    fi

    TEMP_CONFIG=$(mktemp)
    trap 'rm -f "$TEMP_CONFIG"' EXIT
    while IFS= read -r line; do
        if [[ "$line" == *"__SUBNET_VIEWS__"* ]]; then
            printf '%s' "$SUBNET_VIEWS"
        elif [[ "$line" == *"__WORKERS__"* ]]; then
            echo "${line//__WORKERS__/auto}"
        elif [[ "$line" == *"__CACHE_SIZE__"* ]]; then
            echo "${line//__CACHE_SIZE__/$CACHE_SIZE}"
        elif [[ "$line" == *"__LOCAL_DATA__"* ]]; then
            if [[ -n "$LOCAL_DATA" ]]; then
                printf '%s\n' "$LOCAL_DATA"
            fi
        else
            echo "$line"
        fi
    done < config/kresd/config.yaml.template > "$TEMP_CONFIG"
    mv "$TEMP_CONFIG" config/kresd/config.yaml
    ok "Regenerated: config/kresd/config.yaml"
fi

# ---- Step 3: Rebuild custom images ----
info "Rebuilding custom images..."
export APP_VERSION=$(cat VERSION 2>/dev/null || echo "dev")
docker compose build --parallel 2>&1 | tail -5
ok "Images rebuilt (version: ${APP_VERSION})"

# Cleanup: remove dangling images and build cache from previous builds
info "Cleaning up old images and build cache..."
docker image prune -f >/dev/null 2>&1 || true
docker builder prune -f --filter "until=24h" >/dev/null 2>&1 || true
ok "Cleanup done"

# ---- Step 4: Rolling restart ----
info "Restarting services..."

# Restart infra (stock images — just restart, don't recreate)
info "  Restarting infrastructure services..."
docker compose restart clickhouse redis postgres
sleep 3

# Recreate custom-built services with new images
info "  Restarting dnstap-ingester..."
docker compose up -d --no-deps dnstap-ingester
sleep 2

info "  Restarting kresd..."
docker compose up -d --no-deps kresd
sleep 2

info "  Restarting dnsdist (packet cache proxy)..."
docker compose up -d --no-deps dnsdist
# dnsdist resolves the kresd backend IP once at startup. If kresd was
# RECREATED above (new container = possibly new IP), a long-running dnsdist
# keeps forwarding to the dead IP and port 53 goes dark while everything
# reports healthy — this took out 238 (and briefly 216) on 12 Aug 2026.
# `up -d` alone does nothing when dnsdist's own config is unchanged, so
# force a restart to re-resolve.
docker compose restart dnsdist

info "  Restarting monitoring..."
docker compose restart prometheus node-exporter

info "  Restarting frontend..."
docker compose up -d --no-deps frontend

# Restart reverse proxy
info "  Restarting caddy..."
docker compose up -d caddy

# ---- Step 5: Health check (before backend restart) ----
echo ""
info "Running health checks..."
sleep 3

# Check DNS
if command -v dig &>/dev/null; then
    RESULT=$(dig @127.0.0.1 google.com +short +timeout=5 2>/dev/null | head -1 || echo "FAILED")
    if [[ "$RESULT" != "FAILED" && -n "$RESULT" ]]; then
        ok "DNS resolver working (google.com -> ${RESULT})"
    else
        warn "DNS not responding. Check: docker compose logs kresd --tail 20"
    fi
fi

# Check dashboard frontend
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "http://127.0.0.1:3000" 2>/dev/null || echo "000")
if [[ "$HTTP_CODE" == "200" ]]; then
    ok "Dashboard accessible (HTTP ${HTTP_CODE})"
else
    warn "Dashboard returned HTTP ${HTTP_CODE}"
fi

echo ""
echo -e "${GREEN}Update complete!${NC}"
echo "  Dashboard: https://${DOMAIN}"
echo ""

# Restart backend LAST — when running inside the backend container,
# this kills the update process, so everything else must be done first.
# Use nohup so the docker command survives even if this shell is killed.
info "Restarting backend container..."
nohup docker compose up -d backend >/dev/null 2>&1 &
