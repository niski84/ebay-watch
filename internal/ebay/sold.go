package ebay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"os/exec"
)

// SoldListing is one sold item from the completed-listings SERP.
type SoldListing struct {
	Title    string `json:"title"`
	Price    int    `json:"price"`
	URL      string `json:"url"`
	ImageURL string `json:"image_url,omitempty"`
}

// SoldResult holds market pricing data derived from eBay completed/sold listings.
type SoldResult struct {
	Median   int           `json:"median"`
	Count    int           `json:"count"`
	Prices   []int         `json:"prices"`
	Listings []SoldListing `json:"listings"`
}

// MarketPricer fetches sold-listing market price data for a keyword query.
// category is an optional eBay category ID (e.g. "6001" for Cars & Trucks; "" = all categories).
type MarketPricer interface {
	FetchSoldPrices(ctx context.Context, query, category string) (*SoldResult, error)
}

// FetchSoldPrices implements MarketPricer for Playwright by running ebay-sold.mjs.
func (p *Playwright) FetchSoldPrices(ctx context.Context, query, category string) (*SoldResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("empty query")
	}

	// Derive sold script path from search script path (same directory).
	soldScript := filepath.Join(filepath.Dir(p.scriptPath), "ebay-sold.mjs")

	cmd := exec.CommandContext(ctx, p.node, soldScript, query, "25", category)
	cmd.Env = playwrightEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if len(stderr.Bytes()) > 0 {
		fmt.Printf("[sold] %s\n", strings.TrimSpace(stderr.String()))
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("ebay-sold: %s", msg)
	}
	var result SoldResult
	if err := json.Unmarshal(bytes.TrimSpace(out), &result); err != nil {
		return nil, fmt.Errorf("ebay-sold json: %w", err)
	}
	return &result, nil
}

var _ MarketPricer = (*Playwright)(nil)
