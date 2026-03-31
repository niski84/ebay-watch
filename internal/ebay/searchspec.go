package ebay

import (
	"fmt"
	"strings"
)

// SearchSpec tells the scraper how to open eBay SERP.
// When EbayURL is non-empty, it is used as-is (with _ipg applied in the script); Keywords and ItemCondition are ignored for navigation.
type SearchSpec struct {
	Keywords      string
	ItemCondition string // LH_ItemCondition value when using keyword mode (may be pipe-separated codes)
	EbayURL       string
	TitleFilter   string // comma/space-separated keywords; at least one must appear in title+details (post-fetch)
	ExcludeFilter string // comma/space-separated phrases; if any found in title+details the listing is dropped
	MinPrice      string // minimum price (USD); appended as _udlo to eBay URL
	MaxPrice      string // maximum price (USD); appended as _udhi to eBay URL
}

// Validate ensures the spec can run a search.
func (sp SearchSpec) Validate() error {
	if strings.TrimSpace(sp.EbayURL) != "" {
		return nil
	}
	if strings.TrimSpace(sp.Keywords) == "" {
		return fmt.Errorf("empty search query")
	}
	return nil
}
