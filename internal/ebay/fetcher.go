package ebay

// Item is a normalized listing row (from www.ebay.com via scripts/ebay-search.mjs).
type Item struct {
	ItemID         string
	Title          string
	ItemWebURL     string
	ImageURL       string
	ImageURLs      []string // gallery for lightbox; empty means use ImageURL only
	PriceValue     string
	PriceCurrency  string
	Condition      string
	ListingDetails string
	SellerName     string
}

// Searcher loads listing rows for a search (Playwright + public search page).
type Searcher interface {
	Search(spec SearchSpec) ([]Item, error)
}
