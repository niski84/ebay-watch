# ebay-watch

Local dashboard for eBay saved searches: polls the public search results page (Playwright + Node scraper, no official API), stores listings in SQLite, and serves a small web UI.

## Quick start

```bash
cp .env.example .env
npm install
go build -o ebay-watch ./cmd/ebay-watch
./ebay-watch
```

Open `http://127.0.0.1:9109/` (or your `PORT`). Use **Saved searches** to add keyword queries or paste full eBay search URLs.

## Useful env vars

See `.env.example`. Highlights:

- `PLAYWRIGHT_TIMEOUT_SECS` — polling runs SERP + per-item gallery fetches; allow enough time.
- `EBAY_SKIP_ITEM_GALLERY=1` — faster polls, single image per row only.
- `EBAY_PW_HEADLESS=0` — headed browser if eBay shows a bot interstitial.

## Scripts

- `./scripts/reload.sh` — rebuild and restart the binary (expects a running layout like the author’s deploy).
- `./scripts/verify_api.sh` — smoke-check HTTP endpoints.
- `npm run search` — run the scraper standalone (see `scripts/ebay-search.mjs`).

## Legal

This tool automates a normal browser view of public eBay pages. Respect eBay’s terms of use and rate limits; use at your own risk.
