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

// Search is a saved eBay query the poller runs on a schedule.
type Search struct {
	ID                  int64      `json:"id"`
	Query               string     `json:"query"`
	Enabled             bool       `json:"enabled"`
	ShowInResults       bool       `json:"show_in_results"`
	ItemConditionFilter string     `json:"item_condition_filter"`
	ItemConditions      []string   `json:"item_conditions,omitempty"`
	EbaySearchURL       string     `json:"ebay_search_url,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	LastPolledAt        *time.Time `json:"last_polled_at,omitempty"`
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
	dsn := "file:" + filepath.ToSlash(abs) + "?_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
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
	return s.migrateSearchesV2()
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

// GetSearch returns one search by id, or ErrSearchNotFound.
func (s *Store) GetSearch(id int64) (*Search, error) {
	var se Search
	var created string
	var lastPolled sql.NullString
	var en, sir int
	err := s.db.QueryRow(`
SELECT id, query, enabled, IFNULL(show_in_results, 1), IFNULL(item_condition_filter, ''), IFNULL(ebay_search_url, ''), created_at, last_polled_at
FROM searches WHERE id = ?`, id,
	).Scan(&se.ID, &se.Query, &en, &sir, &se.ItemConditionFilter, &se.EbaySearchURL, &created, &lastPolled)
	if err == sql.ErrNoRows {
		return nil, ErrSearchNotFound
	}
	if err != nil {
		return nil, err
	}
	se.Enabled = en != 0
	se.ShowInResults = sir != 0
	t, err := time.Parse(time.RFC3339, created)
	if err != nil {
		return nil, err
	}
	se.CreatedAt = t
	if lastPolled.Valid {
		lp, err := time.Parse(time.RFC3339, lastPolled.String)
		if err != nil {
			return nil, err
		}
		se.LastPolledAt = &lp
	}
	return &se, nil
}

// ListSearches returns all saved searches.
func (s *Store) ListSearches() ([]Search, error) {
	rows, err := s.db.Query(`
SELECT id, query, enabled, IFNULL(show_in_results, 1), IFNULL(item_condition_filter, ''), IFNULL(ebay_search_url, ''), created_at, last_polled_at
FROM searches ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Search
	for rows.Next() {
		var (
			se         Search
			created    string
			lastPolled sql.NullString
			en         int
			sir        int
		)
		if err := rows.Scan(&se.ID, &se.Query, &en, &sir, &se.ItemConditionFilter, &se.EbaySearchURL, &created, &lastPolled); err != nil {
			return nil, err
		}
		se.Enabled = en != 0
		se.ShowInResults = sir != 0
		t, err := time.Parse(time.RFC3339, created)
		if err != nil {
			return nil, err
		}
		se.CreatedAt = t
		if lastPolled.Valid {
			lp, err := time.Parse(time.RFC3339, lastPolled.String)
			if err != nil {
				return nil, err
			}
			se.LastPolledAt = &lp
		}
		out = append(out, se)
	}
	return out, rows.Err()
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
func (s *Store) UpsertListing(searchID int64, ebayItemID, title, priceValue, priceCurrency, imageURL, imageGalleryJSON, itemWebURL, condition, listingDetails string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`
INSERT INTO listings (ebay_item_id, search_id, title, item_condition, listing_details, price_value, price_currency, image_url, image_gallery, item_web_url, fetched_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(search_id, ebay_item_id) DO UPDATE SET
  title = excluded.title,
  item_condition = excluded.item_condition,
  listing_details = excluded.listing_details,
  price_value = excluded.price_value,
  price_currency = excluded.price_currency,
  image_url = excluded.image_url,
  image_gallery = excluded.image_gallery,
  item_web_url = excluded.item_web_url,
  fetched_at = excluded.fetched_at
`, ebayItemID, searchID, title, condition, listingDetails, priceValue, priceCurrency, imageURL, imageGalleryJSON, itemWebURL, now)
	return err
}

// ListVisibleListings returns cached items excluding globally rejected ids.
func (s *Store) ListVisibleListings() ([]Listing, error) {
	rows, err := s.db.Query(`
SELECT l.ebay_item_id, l.search_id, s.query, l.title, IFNULL(l.item_condition, ''), IFNULL(l.listing_details, ''),
  l.price_value, l.price_currency, l.image_url, IFNULL(l.image_gallery, ''), l.item_web_url, l.fetched_at,
  CASE WHEN EXISTS (SELECT 1 FROM item_seen v WHERE v.ebay_item_id = l.ebay_item_id) THEN 1 ELSE 0 END
FROM listings l
JOIN searches s ON s.id = l.search_id
WHERE IFNULL(s.show_in_results, 1) != 0
  AND NOT EXISTS (SELECT 1 FROM rejects r WHERE r.ebay_item_id = l.ebay_item_id)
ORDER BY l.fetched_at DESC, l.id DESC
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanListings(rows)
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
			&l.PriceValue, &l.PriceCurrency, &l.ImageURL, &galleryRaw, &l.ItemWebURL, &fetched,
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

// ListEnabledSearches returns id and query for polling.
func (s *Store) ListEnabledSearches() ([]Search, error) {
	rows, err := s.db.Query(`
SELECT id, query, enabled, IFNULL(show_in_results, 1), IFNULL(item_condition_filter, ''), IFNULL(ebay_search_url, ''), created_at, last_polled_at
FROM searches WHERE enabled = 1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Search
	for rows.Next() {
		var (
			se         Search
			created    string
			lastPolled sql.NullString
			en         int
			sir        int
		)
		if err := rows.Scan(&se.ID, &se.Query, &en, &sir, &se.ItemConditionFilter, &se.EbaySearchURL, &created, &lastPolled); err != nil {
			return nil, err
		}
		se.Enabled = en != 0
		se.ShowInResults = sir != 0
		t, err := time.Parse(time.RFC3339, created)
		if err != nil {
			return nil, err
		}
		se.CreatedAt = t
		if lastPolled.Valid {
			lp, err := time.Parse(time.RFC3339, lastPolled.String)
			if err != nil {
				return nil, err
			}
			se.LastPolledAt = &lp
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
