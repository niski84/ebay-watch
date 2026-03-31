#!/bin/bash
set -euo pipefail

_reload_ok_double_beep() {
	local _n
	for _n in 1 2; do
		printf '\a' 2>/dev/null || true
		sleep 0.12
	done
}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
BINARY="$PROJECT_DIR/ebay-watch"

echo "=== ebay-watch reload ==="

echo "→ Stopping existing ebay-watch..."
pkill -f "${BINARY}" 2>/dev/null && sleep 1 || echo "  (none running)"

echo "→ Building cmd/ebay-watch..."
cd "$PROJECT_DIR"
go build -o "$BINARY" ./cmd/ebay-watch
echo "  Build OK: $BINARY"

echo "→ Starting ebay-watch..."
# ── vault: pull fresh secrets from Infisical before sourcing .env ─────────────
_VAULT_SYNC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && cd ../../infrastructure && pwd)/sync-secrets.sh"
if [[ -f "$_VAULT_SYNC" ]] && [[ -n "${INFISICAL_CLIENT_ID:-}" ]]; then
  echo "→ Pulling secrets from vault (ebay-watch)..."
  "$_VAULT_SYNC" --pull ebay-watch 2>/dev/null || echo "  ⚠  Vault pull skipped (using cached .env)"
fi
# ─────────────────────────────────────────────────────────────────────────────

if [ -f "$PROJECT_DIR/.env" ]; then
    set -a
    # shellcheck disable=SC1090
    source "$PROJECT_DIR/.env"
    set +a
fi

PORT="${PORT:-9109}"
export PORT

# Match plex-dashboard: shared Playwright browser cache when unset (same as ~/.cache/ms-playwright).
if [ -z "${PLAYWRIGHT_BROWSERS_PATH:-}" ] && [ -d "${HOME}/.cache/ms-playwright" ]; then
    export PLAYWRIGHT_BROWSERS_PATH="${HOME}/.cache/ms-playwright"
fi

# Optional: visible Firefox for debugging (eBay interstitial / layout). Set in .env or export before reload:
#   EBAY_PW_HEADLESS=0
#   EBAY_PW_SLOWMO_MS=100

nohup "$BINARY" >"$PROJECT_DIR/server.log" 2>&1 &
NEW_PID=$!
echo "$NEW_PID" >"$PROJECT_DIR/server.pid"

for _i in $(seq 1 30); do
    sleep 0.3
    if curl -fsS "http://127.0.0.1:${PORT}/api/health" 2>/dev/null | grep -q '"service":"ebay-watch"'; then
        echo "✓ Server running at http://127.0.0.1:${PORT}/ (PID $NEW_PID)"
        _reload_ok_double_beep
        exit 0
    fi
done

echo "✗ Server did not respond within 9s — check server.log"
exit 1
