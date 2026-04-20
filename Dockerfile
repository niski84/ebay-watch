# Stage 1: Build the Go binary
FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# modernc.org/sqlite is a pure-Go SQLite driver — CGO_ENABLED=0 works fine
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X main.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -o /ebay-watch ./cmd/ebay-watch

# Stage 2: Runtime image (Node.js + Playwright browsers + Go binary)
FROM ubuntu:22.04
ENV DEBIAN_FRONTEND=noninteractive

# Install Node.js 20.x
RUN apt-get update && apt-get install -y curl ca-certificates && \
    curl -fsSL https://deb.nodesource.com/setup_20.x | bash - && \
    apt-get install -y nodejs && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Install Playwright and browser system dependencies
COPY package.json package-lock.json ./
RUN npm ci
RUN npx playwright install --with-deps firefox chromium

# Copy compiled Go binary and static assets
COPY --from=builder /ebay-watch /usr/local/bin/ebay-watch
COPY scripts/ scripts/
COPY web/ web/
COPY config/ config/

# Persistent data volume (SQLite database lives here)
RUN mkdir -p /data
ENV EBAY_WATCH_DATA_DIR=/data
ENV PORT=8080
ENV EBAY_PW_HEADLESS=1

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=10s --start-period=20s --retries=3 \
    CMD curl -f http://localhost:8080/api/health || exit 1

CMD ["ebay-watch"]
