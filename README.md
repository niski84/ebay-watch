# ebay-watch

Self-hosted dashboard for monitoring eBay saved searches without using the eBay API.

## What It Does

ebay-watch polls public eBay search result pages on a schedule, extracts listings, and stores them in SQLite. A small Go web UI shows new listings highlighted, lets you reject items you don't want to see again, and surfaces median sold-price stats by year from completed-listing searches.

Scraping is done by a Node.js Playwright script driving Firefox; the Go server runs the poll loop, serves the HTTP API, and owns the database. Rejected items are fingerprinted by SHA-256 hashes of their photos, so when a seller relists the same item under a new ID it gets auto-rejected on the next poll.

You can track plain keywords or full eBay search URLs. Searches can be edited live in the UI or seeded from `config/searches.md` on first launch.

## Tech Stack

- Go (single binary) with `modernc.org/sqlite` (pure-Go, no CGO)
- Node.js + Playwright (Firefox) for scraping
- Vanilla web UI with dark/light theme support
- Optional HTTP Basic Auth (single user or multiple accounts)

## Installation

Docker Compose is the easiest path:

```bash
git clone https://github.com/niski84/ebay-watch.git
cd ebay-watch
docker compose up -d
```

Open `http://localhost:8080`. The SQLite DB lives in the `ebay-watch-data` volume.

Local development:

```bash
cp .env.example .env
npm install
go build -o ebay-watch ./cmd/ebay-watch
./ebay-watch
```

## Configuration

Environment variables:

- `PORT` (default `8080`) - HTTP listen port
- `POLL_INTERVAL_HOURS` (default `4`) - background poll cadence
- `PLAYWRIGHT_TIMEOUT_SECS` (default `400`) - per-poll timeout
- `EBAY_PW_HEADLESS` (default `1`) - headless browser
- `EBAY_SKIP_ITEM_GALLERY` - set to `1` for faster polls (one image per item)
- `EBAY_ITEM_GALLERY_CONCURRENCY` (default `3`) - parallel gallery tabs
- `HTTP_AUTH_USER` / `HTTP_AUTH_PASS` - single-user Basic Auth
- `HTTP_AUTH_USERS` - multi-user format `alice:pass1,bob:pass2`

If neither auth variable is set the UI is open to anyone on the port.

## License

MIT
