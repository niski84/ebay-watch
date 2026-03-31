#!/bin/bash
# Deploy ebay-watch to Fly.io (matches repo fly.toml: app ebay-watch, region iad, mount ebay_watch_data).
#
# One-time setup (if not done yet):
#   fly volumes create ebay_watch_data --region iad --size 1
#   fly secrets set HTTP_AUTH_USER='your-user' HTTP_AUTH_PASS='your-secret'
#
# Then deploy (repeat anytime after code changes):
#   ./scripts/deploy-fly.sh
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
exec fly deploy "$@"
