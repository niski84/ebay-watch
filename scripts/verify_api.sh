#!/bin/bash
# Verifies JSON APIs return 200. Start the server first (see scripts/reload.sh).
set -euo pipefail
PORT="${PORT:-9109}"
BASE="http://127.0.0.1:${PORT}"

echo "=== ebay-watch API verify (BASE=${BASE}) ==="

HEALTH="$(curl -fsS "${BASE}/api/health")"
echo "$HEALTH" | grep -q '"service":"ebay-watch"' || { echo "  ✗ /api/health is not ebay-watch (wrong port or other service?)"; exit 1; }
echo "  ✓ /api/health (ebay-watch)"

curl -fsS "${BASE}/api/searches" | tee /dev/null
echo "  ✓ /api/searches"

curl -fsS "${BASE}/api/items" | tee /dev/null
echo "  ✓ /api/items"

curl -fsS "${BASE}/settings" | grep -q "settings-debug.js" || { echo "  ✗ /settings missing settings-debug.js"; exit 1; }
echo "  ✓ /settings (settings page)"

echo "=== all checks passed ==="
