# eBay Watch

A self-hosted dashboard for monitoring eBay saved searches. Polls public eBay search results using a Playwright browser scraper (no API key needed), stores listings in SQLite, and serves a web UI with dark/light theme support.

**Features:**
- Track multiple keyword searches or full eBay search URLs
- New listings glow; seen listings fade; rejected listings are permanently hidden
- Image fingerprinting: rejected item photos are hashed so relisted items auto-reject on future polls
- Market price stats: median sold prices from completed listings, broken down by year
- Optional per-item gallery enrichment (visits each listing page for full photo sets)
- Optional HTTP Basic Auth for shared or remote deployments

---

## Docker (recommended)

The easiest way to run eBay Watch is with Docker Compose.

```bash
git clone https://github.com/YOUR_USERNAME/ebay-watch.git
cd ebay-watch
docker compose up -d
```

Open `http://localhost:8080` in your browser.

The SQLite database is stored in a named Docker volume (`ebay-watch-data`) and persists across restarts.

### Configuration

All settings are passed as environment variables. Edit `docker-compose.yml` to customize:

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | HTTP port inside the container |
| `POLL_INTERVAL_HOURS` | `4` | How often to run background polls |
| `PLAYWRIGHT_TIMEOUT_SECS` | `400` | Timeout for each poll run |
| `EBAY_PW_HEADLESS` | `1` | `1` = headless browser (required in Docker) |
| `EBAY_SKIP_ITEM_GALLERY` | _(unset)_ | Set to `1` for faster polls (one image per item) |
| `EBAY_ITEM_GALLERY_CONCURRENCY` | `3` | Parallel tabs for gallery fetching (1–6) |
| `HTTP_AUTH_USER` / `HTTP_AUTH_PASS` | _(unset)_ | Enable HTTP Basic Auth (single user) |
| `HTTP_AUTH_USERS` | _(unset)_ | Multiple accounts: `alice:pass1,bob:pass2` |

> **Auth note:** If neither `HTTP_AUTH_USER` nor `HTTP_AUTH_USERS` is set, the UI is open to anyone who can reach the port. Set credentials if you expose this on a network.

### Seeding searches on first run

eBay Watch reads `config/searches.md` on first launch to populate the database. Edit the file before starting the container, or bind-mount your own:

```yaml
volumes:
  - ./my-searches.md:/app/config/searches.md:ro
```

Format:

```markdown
## Active searches

- vintage levi denim jacket
- mechanical keyboard tkl
- https://www.ebay.com/sch/i.html?_nkw=leather+boots&LH_ItemCondition=3000
```

You can also add and manage searches at any time through the web UI — no restart needed.

---

## Local development

```bash
cp .env.example .env
npm install          # installs Playwright and browsers
go build -o ebay-watch ./cmd/ebay-watch
./ebay-watch
```

Open `http://127.0.0.1:9109/` (or the `PORT` from your `.env`).

---

## Publishing the Docker image

To build and push to GitHub Container Registry:

```bash
docker build -t ghcr.io/YOUR_GITHUB_USERNAME/ebay-watch:latest .
docker push ghcr.io/YOUR_GITHUB_USERNAME/ebay-watch:latest
```

Then update `docker-compose.yml` to use the published image instead of a local build:

```yaml
image: ghcr.io/YOUR_GITHUB_USERNAME/ebay-watch:latest
# build: .   ← comment this out
```

---

## How it works

1. The background poller runs on the configured interval (default every 4 hours).
2. For each enabled search, a Node.js Playwright script opens Firefox, loads the eBay search results page, and extracts listings via Cheerio.
3. Optionally, each listing's item page is visited to collect additional photos.
4. Results are stored in SQLite. The web UI fetches listings via a small Go HTTP API.

**Image fingerprinting:** When you reject a listing, its images are downloaded and SHA-256 hashed. During future polls, new listings are checked against those hashes — if a match is found the item is silently rejected, even if the seller relisted it with a new item ID.

---

## Legal

This tool automates a normal browser view of public eBay pages. Respect eBay's terms of use and rate limits. Use at your own risk.
