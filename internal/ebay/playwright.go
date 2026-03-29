package ebay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func playwrightEnv() []string {
	env := os.Environ()
	if os.Getenv("PLAYWRIGHT_BROWSERS_PATH") != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return env
	}
	cache := filepath.Join(home, ".cache", "ms-playwright")
	if st, err := os.Stat(cache); err == nil && st.IsDir() {
		return append(env, "PLAYWRIGHT_BROWSERS_PATH="+cache)
	}
	return env
}

// Playwright runs the Node + Playwright scraper script and parses JSON stdout.
type Playwright struct {
	scriptPath string
	node       string
	limit      int
	timeout    time.Duration
}

// NewPlaywright builds a Playwright-backed searcher. scriptPath is absolute or cwd-relative.
func NewPlaywright(scriptPath, node string, limit int, timeout time.Duration) *Playwright {
	if strings.TrimSpace(node) == "" {
		node = "node"
	}
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	if limit <= 0 {
		limit = 50
	}
	return &Playwright{
		scriptPath: scriptPath,
		node:       node,
		limit:      limit,
		timeout:    timeout,
	}
}

// Search runs scripts/ebay-search.mjs with either a pasted eBay URL or keywords + optional LH_ItemCondition.
func (p *Playwright) Search(spec SearchSpec) ([]Item, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	abs := p.scriptPath
	if !filepath.IsAbs(abs) {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		abs = filepath.Join(wd, p.scriptPath)
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()

	var args []string
	if u := strings.TrimSpace(spec.EbayURL); u != "" {
		args = []string{abs, "--url", u, fmt.Sprintf("%d", p.limit)}
	} else {
		args = []string{abs, strings.TrimSpace(spec.Keywords), fmt.Sprintf("%d", p.limit)}
		if pipe := strings.TrimSpace(spec.ItemCondition); pipe != "" {
			args = append(args, pipe)
		}
	}

	cmd := exec.CommandContext(ctx, p.node, args...)
	cmd.Env = playwrightEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("playwright search: %s", msg)
	}
	var raw []struct {
		ItemID         string   `json:"itemId"`
		Title          string   `json:"title"`
		ItemWebURL     string   `json:"itemWebUrl"`
		ImageURL       string   `json:"imageUrl"`
		ImageURLs      []string `json:"imageUrls"`
		PriceValue     string   `json:"priceValue"`
		PriceCurrency  string   `json:"priceCurrency"`
		Condition      string   `json:"condition"`
		ListingDetails string   `json:"listingDetails"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &raw); err != nil {
		return nil, fmt.Errorf("playwright json: %w (stderr=%s)", err, strings.TrimSpace(stderr.String()))
	}
	outItems := make([]Item, 0, len(raw))
	for _, r := range raw {
		if r.ItemID == "" {
			continue
		}
		urls := r.ImageURLs
		if len(urls) == 0 && strings.TrimSpace(r.ImageURL) != "" {
			urls = []string{strings.TrimSpace(r.ImageURL)}
		}
		img := strings.TrimSpace(r.ImageURL)
		if img == "" && len(urls) > 0 {
			img = urls[0]
		}
		outItems = append(outItems, Item{
			ItemID:         r.ItemID,
			Title:          r.Title,
			ItemWebURL:     r.ItemWebURL,
			ImageURL:       img,
			ImageURLs:      urls,
			PriceValue:     r.PriceValue,
			PriceCurrency:  r.PriceCurrency,
			Condition:      r.Condition,
			ListingDetails: r.ListingDetails,
		})
	}
	return outItems, nil
}

var _ Searcher = (*Playwright)(nil)
