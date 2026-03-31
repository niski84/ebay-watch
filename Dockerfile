# Stage 1: Build the Go binary
FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# We use modernc.org/sqlite, so CGO_ENABLED=0 works perfectly
RUN CGO_ENABLED=0 GOOS=linux go build -o /ebay-watch ./cmd/ebay-watch

# Stage 2: Final runtime image (Node.js + Playwright Browsers + Go Binary)
FROM ubuntu:22.04
ENV DEBIAN_FRONTEND=noninteractive
ENV EBAY_PW_HEADLESS=1

# Install Node.js
RUN apt-get update && apt-get install -y curl ca-certificates && \
    curl -fsSL https://deb.nodesource.com/setup_20.x | bash - && \
    apt-get install -y nodejs && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Install Playwright and browser system dependencies
COPY package.json package-lock.json ./
RUN npm ci
RUN npx playwright install --with-deps firefox chromium

# Copy our compiled Go backend and local files
COPY --from=builder /ebay-watch /usr/local/bin/ebay-watch
COPY scripts/ scripts/
COPY web/ web/

# Prepare volume for SQLite and Config
RUN mkdir -p /data /app/config
ENV EBAY_WATCH_DATA_DIR=/data
ENV PORT=8080

EXPOSE 8080

CMD ["ebay-watch"]
