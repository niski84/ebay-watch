package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds runtime settings loaded from the environment.
type Config struct {
	Port              string
	DataDir           string
	WebDir            string
	SearchesMDPath    string
	PollInterval      time.Duration
	SearchLimit       int
	PlaywrightScript  string
	NodePath          string
	PlaywrightTimeout time.Duration
}

func getenv(key, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}

// Load reads configuration from environment variables.
func Load() (Config, error) {
	port := getenv("PORT", "9109")
	dataDir := getenv("EBAY_WATCH_DATA_DIR", "data")
	searchesPath := getenv("EBAY_WATCH_SEARCHES_MD", "config/searches.md")

	pollHours := 4.0
	if s := os.Getenv("POLL_INTERVAL_HOURS"); s != "" {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return Config{}, fmt.Errorf("POLL_INTERVAL_HOURS: %w", err)
		}
		pollHours = v
	}
	if pollHours <= 0 {
		pollHours = 4
	}

	limit := 50
	if s := os.Getenv("EBAY_SEARCH_LIMIT"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil {
			return Config{}, fmt.Errorf("EBAY_SEARCH_LIMIT: %w", err)
		}
		limit = v
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	// Default allows SERP + per-listing gallery fetches (item pages) without timing out mid-poll.
	pwTimeout := 300 * time.Second
	if s := os.Getenv("PLAYWRIGHT_TIMEOUT_SECS"); s != "" {
		sec, err := strconv.Atoi(s)
		if err != nil {
			return Config{}, fmt.Errorf("PLAYWRIGHT_TIMEOUT_SECS: %w", err)
		}
		if sec > 0 {
			pwTimeout = time.Duration(sec) * time.Second
		}
	}

	cfg := Config{
		Port:              port,
		DataDir:           dataDir,
		WebDir:            getenv("WEB_DIR", "web"),
		SearchesMDPath:    searchesPath,
		PollInterval:      time.Duration(float64(time.Hour) * pollHours),
		SearchLimit:       limit,
		PlaywrightScript:  getenv("EBAY_PLAYWRIGHT_SCRIPT", "scripts/ebay-search.mjs"),
		NodePath:          getenv("NODE_BIN", "node"),
		PlaywrightTimeout: pwTimeout,
	}
	return cfg, nil
}
