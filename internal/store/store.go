package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SoldListing is one sold item from completed listings SERP (for display in the frontend).
type SoldListing struct {
	Title string `json:"title"`
	Price int    `json:"price"`
	URL   string `json:"url"`
}

// Search is a saved eBay query the poller runs on a schedule.
type Search struct {
	ID                  int64      `json:"id"`
	Query               string     `json:"query"`
	Enabled             bool       `json:"enabled"`
	ShowInResults       bool       `json:"show_in_results"`
	ItemConditionFilter string     `json:"item_condition_filter"`
	ItemConditions      []string   `json:"item_conditions,omitempty"`
	EbaySearchURL       string     `json:"ebay_search_url,omitempty"`
	TitleFilter         string     `json:"title_filter,omitempty"`
	ExcludeFilter       string     `json:"exclude_filter,omitempty"`
	MinPrice            string     `json:"min_price,omitempty"`
	MaxPrice            string     `json:"max_price,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	LastPolledAt        *time.Time `json:"last_polled_at,omitempty"`
	MarketPrice         int                 `json:"market_price,omitempty"`           // overall median sold price in whole dollars (0 = unknown)
	MarketPriceCount    int                 `json:"market_price_count,omitempty"`     // number of sold samples (overall)
	MarketPriceAt       *time.Time          `json:"market_price_at,omitempty"`        // when market price was last fetched
	MarketPricesByYear  map[int]int           `json:"market_prices_by_year,omitempty"`  // year → median asking price
	MarketCountsByYear  map[int]int           `json:"market_counts_by_year,omitempty"`  // year → active listing count
	ListingsByYear      map[int][]SoldListing `json:"listings_by_year,omitempty"`       // year → comparable active listings
}

// Listing is a cached item row from the last successful poll for a search.
type Listing struct {
	EbayItemID     string    `json:"ebay_item_id"`
	SearchID       int64     `json:"search_id"`
	SearchQuery    string    `json:"search_query"`
	Title          string    `json:"title"`
	Condition      string    `json:"condition"`
	ListingDetails string    `json:"listing_details"`
	PriceValue     string    `json:"price_value"`
	PriceCurrency  string    `json:"price_currency"`
	ImageURL       string    `json:"image_url"`
	ImageURLs      []string  `json:"image_urls,omitempty"`
	ItemWebURL     string    `json:"item_web_url"`
	SellerName     string    `json:"seller_name,omitempty"`
	SellerFeedback string    `json:"seller_feedback,omitempty"`
	FetchedAt      time.Time `json:"fetched_at"`
	Seen           bool      `json:"seen"`
}

// Store is a SQLite-backed persistence layer.
type Store struct {
	db *sql.DB
}

func Open(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(abs) + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Limit concurrent writers to avoid SQLITE_BUSY under load.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) initSchema() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS searches (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  query TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  show_in_results INTEGER NOT NULL DEFAULT 1,
  item_condition_filter TEXT NOT NULL DEFAULT '',
  ebay_search_url TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  last_polled_at TEXT
);

CREATE TABLE IF NOT EXISTS listings (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ebay_item_id TEXT NOT NULL,
  search_id INTEGER NOT NULL REFERENCES searches(id) ON DELETE CASCADE,
  title TEXT,
  item_condition TEXT NOT NULL DEFAULT '',
  listing_details TEXT NOT NULL DEFAULT '',
  price_value TEXT,
  price_currency TEXT,
  image_url TEXT,
  image_gallery TEXT NOT NULL DEFAULT '',
  item_web_url TEXT,
  fetched_at TEXT NOT NULL,
  UNIQUE (search_id, ebay_item_id)
);

CREATE INDEX IF NOT EXISTS idx_listings_search ON listings(search_id);

CREATE TABLE IF NOT EXISTS rejects (
  ebay_item_id TEXT PRIMARY KEY,
  rejected_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS rejected_sellers (
  seller_name TEXT PRIMARY KEY,
  rejected_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS search_year_market_prices (
  search_id INTEGER NOT NULL REFERENCES searches(id) ON DELETE CASCADE,
  year INTEGER NOT NULL,
  market_price INTEGER NOT NULL DEFAULT 0,
  market_price_count INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (search_id, year)
);

CREATE TABLE IF NOT EXISTS rejected_image_hashes (
  hash TEXT PRIMARY KEY,
  source_item_id TEXT NOT NULL,
  rejected_at TEXT NOT NULL
);
`)
	if err != nil {
		return err
	}
	if err := s.migrateListingsConditionColumns(); err != nil {
		return err
	}
	if err := s.migrateListingsImageGallery(); err != nil {
		return err
	}
	if err := s.migrateSearchShowInResults(); err != nil {
		return err
	}
	if err := s.migrateSearchItemConditionFilter(); err != nil {
		return err
	}
	if err := s.migrateItemSeen(); err != nil {
		return err
	}
	if err := s.migrateSearchesV2(); err != nil {
		return err
	}
	if err := s.migrateSearchFiltersV3(); err != nil {
		return err
	}
	if err := s.migrateListingsSellerName(); err != nil {
		return err
	}
	if err := s.migrateListingsSellerFeedback(); err != nil {
		return err
	}
	if err := s.migrateRejectedImageHashes(); err != nil {
		return err
	}
	if err := s.migrateSearchMarketPrice(); err != nil {
		return err
	}
	return s.migrateYearMarketPricesListings()
}

func (s *Store) migrateSearchesV2() error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_meta WHERE k = 'searches_v2'`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}

	if _, err := s.db.Exec(`ALTER TABLE searches ADD COLUMN ebay_search_url TEXT NOT NULL DEFAULT ''`); err != nil {
		msg := strings.ToLower(err.Error())
		if !strings.Contains(msg, "duplicate column") {
			return err
		}
	}

	var createSQL string
	if err := s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='searches'`).Scan(&createSQL); err != nil {
		return err
	}
	needsRebuild := strings.Contains(createSQL, "query TEXT NOT NULL UNIQUE") ||
		strings.Contains(createSQL, "UNIQUE (query)") ||
		strings.Contains(createSQL, "unique(query")

	if !needsRebuild {
		_, err := s.db.Exec(`INSERT INTO schema_meta (k, v) VALUES ('searches_v2', '1')`)
		return err
	}

	if _, err := s.db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	defer func() { _, _ = s.db.Exec(`PRAGMA foreign_keys = ON`) }()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`
CREATE TABLE searches_rebuild (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  query TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  show_in_results INTEGER NOT NULL DEFAULT 1,
  item_condition_filter TEXT NOT NULL DEFAULT '',
  ebay_search_url TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  last_polled_at TEXT
);`); err != nil {
		return err
	}

	if _, err := tx.Exec(`
INSERT INTO searches_rebuild (id, query, enabled, show_in_results, item_condition_filter, ebay_search_url, created_at, last_polled_at)
SELECT id, query, enabled, IFNULL(show_in_results, 1), IFNULL(item_condition_filter, ''),
  IFNULL(ebay_search_url, ''), created_at, last_polled_at
FROM searches`); err != nil {
		return err
	}

	if _, err := tx.Exec(`DROP TABLE searches`); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE searches_rebuild RENAME TO searches`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO schema_meta (k, v) VALUES ('searches_v2', '1')`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) migrateSearchFiltersV3() error {
	for _, q := range []string{
		`ALTER TABLE searches ADD COLUMN title_filter TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE searches ADD COLUMN exclude_filter TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE searches ADD COLUMN min_price TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE searches ADD COLUMN max_price TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			msg := strings.ToLower(err.Error())
			if !strings.Contains(msg, "duplicate column") {
				return err
			}
		}
	}
	return nil
}

func (s *Store) migrateListingsSellerName() error {
	if _, err := s.db.Exec(`ALTER TABLE listings ADD COLUMN seller_name TEXT NOT NULL DEFAULT ''`); err != nil {
		msg := strings.ToLower(err.Error())
		if !strings.Contains(msg, "duplicate column") {
			return err
		}
	}
	return nil
}

func (s *Store) migrateListingsSellerFeedback() error {
	if _, err := s.db.Exec(`ALTER TABLE listings ADD COLUMN seller_feedback TEXT NOT NULL DEFAULT ''`); err != nil {
		msg := strings.ToLower(err.Error())
		if !strings.Contains(msg, "duplicate column") {
			return err
		}
	}
	return nil
}

func (s *Store) migrateSearchMarketPrice() error {
	for _, q := range []string{
		`ALTER TABLE searches ADD COLUMN market_price INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE searches ADD COLUMN market_price_count INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE searches ADD COLUMN market_price_at TEXT`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			if !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
				return err
			}
		}
	}
	return nil
}

func (s *Store) migrateYearMarketPricesListings() error {
	if _, err := s.db.Exec(`ALTER TABLE search_year_market_prices ADD COLUMN sold_listings_json TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return err
		}
	}
	return nil
}

func (s *Store) migrateRejectedImageHashes() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS rejected_image_hashes (
  hash TEXT PRIMARY KEY,
  source_item_id TEXT NOT NULL,
  rejected_at TEXT NOT NULL
);`)
	return err
}

func (s *Store) migrateItemSeen() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS item_seen (
  ebay_item_id TEXT PRIMARY KEY,
  seen_at TEXT NOT NULL
);`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_meta (
  k TEXT PRIMARY KEY NOT NULL,
  v TEXT NOT NULL
);`); err != nil {
		return err
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_meta WHERE k = 'item_seen_backfill_v1'`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.db.Exec(`
INSERT OR IGNORE INTO item_seen (ebay_item_id, seen_at)
SELECT DISTINCT ebay_item_id, ? FROM listings WHERE TRIM(COALESCE(ebay_item_id, '')) != ''
`, now); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO schema_meta (k, v) VALUES ('item_seen_backfill_v1', '1')`)
	return err
}

func (s *Store) migrateSearchItemConditionFilter() error {
	q := `ALTER TABLE searches ADD COLUMN item_condition_filter TEXT NOT NULL DEFAULT ''`
	if _, err := s.db.Exec(q); err != nil {
		msg := strings.ToLower(err.Error())
		if !strings.Contains(msg, "duplicate column") {
			return err
		}
	}
	return nil
}

func (s *Store) migrateSearchShowInResults() error {
	q := `ALTER TABLE searches ADD COLUMN show_in_results INTEGER NOT NULL DEFAULT 1`
	if _, err := s.db.Exec(q); err != nil {
		msg := strings.ToLower(err.Error())
		if !strings.Contains(msg, "duplicate column") {
			return err
		}
	}
	return nil
}

func (s *Store) migrateListingsConditionColumns() error {
	for _, q := range []string{
		`ALTER TABLE listings ADD COLUMN item_condition TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE listings ADD COLUMN listing_details TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			msg := strings.ToLower(err.Error())
			if !strings.Contains(msg, "duplicate column") {
				return err
			}
		}
	}
	return nil
}

func (s *Store) migrateListingsImageGallery() error {
	if _, err := s.db.Exec(`ALTER TABLE listings ADD COLUMN image_gallery TEXT NOT NULL DEFAULT ''`); err != nil {
		msg := strings.ToLower(err.Error())
		if !strings.Contains(msg, "duplicate column") {
			return err
		}
	}
	return nil
}

func listingImageURLs(galleryJSON, imageURL string) []string {
	galleryJSON = strings.TrimSpace(galleryJSON)
	if galleryJSON != "" {
		var u []string
		if json.Unmarshal([]byte(galleryJSON), &u) == nil {
			out := make([]string, 0, len(u))
			for _, s := range u {
				s = strings.TrimSpace(s)
				if s != "" {
					out = append(out, s)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	if strings.TrimSpace(imageURL) != "" {
		return []string{strings.TrimSpace(imageURL)}
	}
	return nil
}

// SeedSearches inserts queries if they are not already present.
func (s *Store) SeedSearches(queries []string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	for _, q := range queries {
		if q == "" {
			continue
		}
		_, err := s.db.Exec(
			`INSERT OR IGNORE INTO searches (query, enabled, created_at, show_in_results, item_condition_filter, ebay_search_url) VALUES (?, 1, ?, 1, '', '')`,
			q, now,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

const searchSelectCols = `
SELECT id, query, enabled, IFNULL(show_in_results,1), IFNULL(item_condition_filter,''), IFNULL(ebay_search_url,''),
  IFNULL(title_filter,''), IFNULL(exclude_filter,''), IFNULL(min_price,''), IFNULL(max_price,''),
  created_at, last_polled_at,
  IFNULL(market_price,0), IFNULL(market_price_count,0), market_price_at`

func scanSearch(row interface {
	Scan(...any) error
}) (Search, error) {
	var se Search
	var created string
	var lastPolled, marketPriceAt sql.NullString
	var en, sir int
	if err := row.Scan(
		&se.ID, &se.Query, &en, &sir, &se.ItemConditionFilter, &se.EbaySearchURL,
		&se.TitleFilter, &se.ExcludeFilter, &se.MinPrice, &se.MaxPrice,
		&created, &lastPolled,
		&se.MarketPrice, &se.MarketPriceCount, &marketPriceAt,
	); err != nil {
		return se, err
	}
	se.Enabled = en != 0
	se.ShowInResults = sir != 0
	t, err := time.Parse(time.RFC3339, created)
	if err != nil {
		return se, err
	}
	se.CreatedAt = t
	if lastPolled.Valid {
		lp, err := time.Parse(time.RFC3339, lastPolled.String)
		if err != nil {
			return se, err
		}
		se.LastPolledAt = &lp
	}
	if marketPriceAt.Valid {
		mp, err := time.Parse(time.RFC3339, marketPriceAt.String)
		if err == nil {
			se.MarketPriceAt = &mp
		}
	}
	return se, nil
}

// GetSearch returns one search by id, or ErrSearchNotFound.
func (s *Store) GetSearch(id int64) (*Search, error) {
	row := s.db.QueryRow(searchSelectCols+` FROM searches WHERE id = ?`, id)
	se, err := scanSearch(row)
	if err == sql.ErrNoRows {
		return nil, ErrSearchNotFound
	}
	if err != nil {
		return nil, err
	}
	return &se, nil
}

// ListSearches returns all saved searches.
func (s *Store) ListSearches() ([]Search, error) {
	rows, err := s.db.Query(searchSelectCols + ` FROM searches ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Search
	for rows.Next() {
		se, err := scanSearch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, se)
	}
	return out, rows.Err()
}

// SetSearchMarketPrice stores median sold price (in whole dollars) and sample count for a search.
func (s *Store) SetSearchMarketPrice(id int64, price, count int) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(
		`UPDATE searches SET market_price=?, market_price_count=?, market_price_at=? WHERE id=?`,
		price, count, now, id,
	)
	return err
}

// SetSearchYearMarketPrice stores the median sold price for a specific model year within a search.
// listingsJSON is a JSON array of SoldListing objects (may be empty string).
func (s *Store) SetSearchYearMarketPrice(searchID int64, year, price, count int, listingsJSON string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`
INSERT INTO search_year_market_prices (search_id, year, market_price, market_price_count, sold_listings_json, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(search_id, year) DO UPDATE SET
  market_price=excluded.market_price,
  market_price_count=excluded.market_price_count,
  sold_listings_json=excluded.sold_listings_json,
  updated_at=excluded.updated_at`,
		searchID, year, price, count, listingsJSON, now,
	)
	return err
}

// GetSearchYearMarketPrices returns prices, counts, and sold listings keyed by year for a search.
func (s *Store) GetSearchYearMarketPrices(searchID int64) (prices map[int]int, counts map[int]int, listings map[int][]SoldListing, err error) {
	rows, err := s.db.Query(
		`SELECT year, market_price, market_price_count, IFNULL(sold_listings_json,'') FROM search_year_market_prices WHERE search_id=? AND market_price>0`,
		searchID,
	)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	prices = make(map[int]int)
	counts = make(map[int]int)
	listings = make(map[int][]SoldListing)
	for rows.Next() {
		var year, price, count int
		var listingsJSON string
		if err := rows.Scan(&year, &price, &count, &listingsJSON); err != nil {
			return nil, nil, nil, err
		}
		prices[year] = price
		counts[year] = count
		if listingsJSON != "" {
			var sl []SoldListing
			if json.Unmarshal([]byte(listingsJSON), &sl) == nil {
				listings[year] = sl
			}
		}
	}
	return prices, counts, listings, rows.Err()
}

// AddSearch creates a new search row. When ebaySearchURL is set, itemConditionFilter is ignored for scraping.
func (s *Store) AddSearch(query, itemConditionFilter, ebaySearchURL string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.Exec(
		`INSERT INTO searches (query, enabled, created_at, show_in_results, item_condition_filter, ebay_search_url) VALUES (?, 1, ?, 1, ?, ?)`,
		query, now, itemConditionFilter, ebaySearchURL,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteSearch removes a search and its listing rows.
func (s *Store) DeleteSearch(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM listings WHERE search_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM searches WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// SetSearchShowInResults sets whether listings from this search appear in the combined results view.
func (s *Store) SetSearchShowInResults(id int64, show bool) error {
	v := 0
	if show {
		v = 1
	}
	res, err := s.db.Exec(`UPDATE searches SET show_in_results = ? WHERE id = ?`, v, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrSearchNotFound
	}
	return nil
}

// SetSearchItemConditionFilter sets the eBay LH_ItemCondition pipe string (empty = any condition).
func (s *Store) SetSearchItemConditionFilter(id int64, itemConditionFilter string) error {
	res, err := s.db.Exec(`UPDATE searches SET item_condition_filter = ? WHERE id = ?`, itemConditionFilter, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrSearchNotFound
	}
	return nil
}

// SetSearchTitleFilter sets the title keyword filter (comma/space-separated; any term must match).
func (s *Store) SetSearchTitleFilter(id int64, filter string) error {
	res, err := s.db.Exec(`UPDATE searches SET title_filter = ? WHERE id = ?`, strings.TrimSpace(filter), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrSearchNotFound
	}
	return nil
}

// SetSearchPriceRange sets the min/max price filter (empty string = no limit).
func (s *Store) SetSearchPriceRange(id int64, minPrice, maxPrice string) error {
	res, err := s.db.Exec(`UPDATE searches SET min_price = ?, max_price = ? WHERE id = ?`,
		strings.TrimSpace(minPrice), strings.TrimSpace(maxPrice), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrSearchNotFound
	}
	return nil
}

// SetSearchExcludeFilter sets terms that, if found in title/details, cause the listing to be dropped.
func (s *Store) SetSearchExcludeFilter(id int64, filter string) error {
	res, err := s.db.Exec(`UPDATE searches SET exclude_filter = ? WHERE id = ?`, strings.TrimSpace(filter), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrSearchNotFound
	}
	return nil
}

// SetSearchQuery updates the keyword query for a search (keyword-mode only).
func (s *Store) SetSearchQuery(id int64, query string) error {
	query = strings.TrimSpace(query)
	if query == "" {
		return fmt.Errorf("query cannot be empty")
	}
	res, err := s.db.Exec(`UPDATE searches SET query = ? WHERE id = ?`, query, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrSearchNotFound
	}
	return nil
}

// MarkSearchPolled updates last_polled_at for a search.
func (s *Store) MarkSearchPolled(id int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`UPDATE searches SET last_polled_at = ? WHERE id = ?`, now, id)
	return err
}

// Reject persists a user dismissal for an item id (global hide) and drops cached listing
// payload (images, title, etc.) so the DB stays small. Only the rejects row is kept so the
// item stays out of the grid; polling skips re-upserting rejected ids.
func (s *Store) Reject(ebayItemID string) error {
	if err := ValidateItemID(ebayItemID); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT OR REPLACE INTO rejects (ebay_item_id, rejected_at) VALUES (?, ?)`, ebayItemID, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM listings WHERE ebay_item_id = ?`, ebayItemID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM item_seen WHERE ebay_item_id = ?`, ebayItemID); err != nil {
		return err
	}
	return tx.Commit()
}

// RejectedEbayItemIDs returns every rejected item id for poll-time upsert filtering.
func (s *Store) RejectedEbayItemIDs() (map[string]struct{}, error) {
	rows, err := s.db.Query(`SELECT ebay_item_id FROM rejects`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		id = strings.TrimSpace(id)
		if id != "" {
			out[id] = struct{}{}
		}
	}
	return out, rows.Err()
}

// RejectSeller blocks all listings from a given eBay seller name globally.
func (s *Store) RejectSeller(sellerName string) error {
	sellerName = strings.TrimSpace(sellerName)
	if sellerName == "" {
		return fmt.Errorf("seller_name: %w", ErrEmpty)
	}
	if len(sellerName) > 256 {
		return fmt.Errorf("seller_name too long")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`INSERT OR REPLACE INTO rejected_sellers (seller_name, rejected_at) VALUES (?, ?)`, sellerName, now)
	return err
}

// RejectedSellerNames returns every blocked seller name for poll-time filtering.
func (s *Store) RejectedSellerNames() (map[string]struct{}, error) {
	rows, err := s.db.Query(`SELECT seller_name FROM rejected_sellers`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]struct{})
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		name = strings.TrimSpace(name)
		if name != "" {
			out[name] = struct{}{}
		}
	}
	return out, rows.Err()
}

// GetListingImageURLs returns the primary and gallery image URLs for an item (used before deletion).
func (s *Store) GetListingImageURLs(ebayItemID string) ([]string, error) {
	var imageURL, galleryJSON string
	err := s.db.QueryRow(
		`SELECT IFNULL(image_url,''), IFNULL(image_gallery,'') FROM listings WHERE ebay_item_id = ? LIMIT 1`,
		ebayItemID,
	).Scan(&imageURL, &galleryJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return listingImageURLs(galleryJSON, imageURL), nil
}

// StoreImageHashes persists content hashes for images belonging to a rejected item.
func (s *Store) StoreImageHashes(hashes []string, sourceItemID string) error {
	if len(hashes) == 0 {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, h := range hashes {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if _, err := s.db.Exec(
			`INSERT OR IGNORE INTO rejected_image_hashes (hash, source_item_id, rejected_at) VALUES (?, ?, ?)`,
			h, sourceItemID, now,
		); err != nil {
			return err
		}
	}
	return nil
}

// RejectedImageHashSet returns all stored image hashes as a set for O(1) poll-time lookup.
func (s *Store) RejectedImageHashSet() (map[string]struct{}, error) {
	rows, err := s.db.Query(`SELECT hash FROM rejected_image_hashes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]struct{})
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		if h != "" {
			out[h] = struct{}{}
		}
	}
	return out, rows.Err()
}

// MarkItemSeen records that the user has reviewed this listing (global per eBay item id).
func (s *Store) MarkItemSeen(ebayItemID string) error {
	if err := ValidateItemID(ebayItemID); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`INSERT OR REPLACE INTO item_seen (ebay_item_id, seen_at) VALUES (?, ?)`, ebayItemID, now)
	return err
}

// UpsertListing stores or refreshes a listing for a search.
// imageGalleryJSON is a JSON array of image URLs (empty if none); imageURL should be the primary thumbnail (first gallery URL is typical).
func (s *Store) UpsertListing(searchID int64, ebayItemID, title, priceValue, priceCurrency, imageURL, imageGalleryJSON, itemWebURL, condition, listingDetails, sellerName, sellerFeedback string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`
INSERT INTO listings (ebay_item_id, search_id, title, item_condition, listing_details, price_value, price_currency, image_url, image_gallery, item_web_url, seller_name, seller_feedback, fetched_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(search_id, ebay_item_id) DO UPDATE SET
  title = excluded.title,
  item_condition = excluded.item_condition,
  listing_details = excluded.listing_details,
  price_value = excluded.price_value,
  price_currency = excluded.price_currency,
  image_url = excluded.image_url,
  image_gallery = excluded.image_gallery,
  item_web_url = excluded.item_web_url,
  seller_name = excluded.seller_name,
  seller_feedback = excluded.seller_feedback,
  fetched_at = excluded.fetched_at
`, ebayItemID, searchID, title, condition, listingDetails, priceValue, priceCurrency, imageURL, imageGalleryJSON, itemWebURL, sellerName, sellerFeedback, now)
	return err
}

// ListVisibleListings returns cached items excluding globally rejected ids and
// items matching any search-level exclude_filter.
func (s *Store) ListVisibleListings() ([]Listing, error) {
	rows, err := s.db.Query(`
SELECT l.ebay_item_id, l.search_id, s.query, l.title, IFNULL(l.item_condition, ''), IFNULL(l.listing_details, ''),
  l.price_value, l.price_currency, l.image_url, IFNULL(l.image_gallery, ''), l.item_web_url, IFNULL(l.seller_name, ''), IFNULL(l.seller_feedback, ''), l.fetched_at,
  CASE WHEN EXISTS (SELECT 1 FROM item_seen v WHERE v.ebay_item_id = l.ebay_item_id) THEN 1 ELSE 0 END
FROM listings l
JOIN searches s ON s.id = l.search_id
WHERE NOT EXISTS (SELECT 1 FROM rejects r WHERE r.ebay_item_id = l.ebay_item_id)
  AND NOT EXISTS (SELECT 1 FROM rejected_sellers rs WHERE rs.seller_name = l.seller_name AND l.seller_name != '')
ORDER BY l.fetched_at DESC, l.id DESC
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	all, err := scanListings(rows)
	if err != nil {
		return nil, err
	}
	// Build a map of search_id → exclude terms
	searches, err := s.ListSearches()
	if err != nil {
		return all, nil // degrade gracefully
	}
	excludeBySearch := make(map[int64][]string)
	for _, se := range searches {
		ef := strings.TrimSpace(se.ExcludeFilter)
		if ef == "" {
			continue
		}
		var terms []string
		for _, t := range strings.Split(ef, ",") {
			if t = strings.TrimSpace(t); t != "" {
				terms = append(terms, strings.ToLower(t))
			}
		}
		if len(terms) > 0 {
			excludeBySearch[se.ID] = terms
		}
	}
	if len(excludeBySearch) == 0 {
		return all, nil
	}
	filtered := make([]Listing, 0, len(all))
	for _, l := range all {
		terms, ok := excludeBySearch[l.SearchID]
		if !ok {
			filtered = append(filtered, l)
			continue
		}
		haystack := strings.ToLower(l.Title + " " + l.ListingDetails)
		excluded := false
		for _, t := range terms {
			if strings.Contains(haystack, t) {
				excluded = true
				break
			}
		}
		if !excluded {
			filtered = append(filtered, l)
		}
	}
	return filtered, nil
}

func scanListings(rows *sql.Rows) ([]Listing, error) {
	out := []Listing{}
	for rows.Next() {
		var (
			l          Listing
			galleryRaw string
			fetched    string
			seen       int
		)
		if err := rows.Scan(
			&l.EbayItemID, &l.SearchID, &l.SearchQuery, &l.Title,
			&l.Condition, &l.ListingDetails,
			&l.PriceValue, &l.PriceCurrency, &l.ImageURL, &galleryRaw, &l.ItemWebURL, &l.SellerName, &l.SellerFeedback, &fetched,
			&seen,
		); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339, fetched)
		if err != nil {
			return nil, err
		}
		l.FetchedAt = t
		l.Seen = seen != 0
		l.ImageURLs = listingImageURLs(galleryRaw, l.ImageURL)
		out = append(out, l)
	}
	return out, rows.Err()
}

// TotalSearchRows returns how many search rows exist (any enabled flag).
func (s *Store) TotalSearchRows() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM searches`).Scan(&n)
	return n, err
}

// ListEnabledSearches returns enabled searches for polling.
func (s *Store) ListEnabledSearches() ([]Search, error) {
	rows, err := s.db.Query(searchSelectCols + ` FROM searches WHERE enabled = 1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Search
	for rows.Next() {
		se, err := scanSearch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, se)
	}
	return out, rows.Err()
}

// ErrEmpty is returned when a required identifier is missing.
var ErrEmpty = errors.New("empty value")

// ErrSearchNotFound is returned when updating a non-existent search id.
var ErrSearchNotFound = errors.New("search not found")

// ValidateItemID rejects obviously bad input.
func ValidateItemID(id string) error {
	if id == "" {
		return fmt.Errorf("ebay_item_id: %w", ErrEmpty)
	}
	if len(id) > 256 {
		return fmt.Errorf("ebay_item_id too long")
	}
	return nil
}
