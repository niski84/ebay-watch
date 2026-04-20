package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"ebay-watch/internal/config"
	"ebay-watch/internal/ebay"
	"ebay-watch/internal/imghash"
	"ebay-watch/internal/poller"
	"ebay-watch/internal/store"
)

// Server wires HTTP handlers to the store and listing fetcher.
type Server struct {
	cfg        config.Config
	store      *store.Store
	search     ebay.Searcher
	fetchMode  string
	buildTime  string
	pollMu     sync.Mutex
	pollActive bool
}

// New creates an HTTP server facade.
func New(cfg config.Config, st *store.Store, search ebay.Searcher, fetchMode string, buildTime string) *Server {
	return &Server{cfg: cfg, store: st, search: search, fetchMode: fetchMode, buildTime: buildTime}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/searches", s.handleSearches)
	mux.HandleFunc("/api/items", s.handleItems)
	mux.HandleFunc("/api/reject", s.handleReject)
	mux.HandleFunc("/api/reject-seller", s.handleRejectSeller)
	mux.HandleFunc("/api/seen", s.handleSeen)
	mux.HandleFunc("/api/poll", s.handlePoll)
	mux.HandleFunc("/api/market-lookup", s.handleMarketLookup)
	mux.HandleFunc("/api/item-image", s.handleItemImage)
	mux.HandleFunc("/settings", s.handleSettingsPage)

	fs := http.FileServer(http.Dir(s.cfg.WebDir))
	mux.Handle("/", fs)
	return logRequests(basicAuth(mux, "/api/", "/settings"))
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Printf("[API] %s %s\n", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

// basicAuth builds a credentials map from:
//   - HTTP_AUTH_USER / HTTP_AUTH_PASS  (original single-user env vars)
//   - HTTP_AUTH_USERS                  (comma-separated "user:pass,user2:pass2")
// basicAuth enforces HTTP Basic Auth only on paths that start with one of the
// given prefixes. Static files are intentionally left open so the login page
// can load before credentials are available.
func basicAuth(next http.Handler, protectedPrefixes ...string) http.Handler {
	creds := map[string]string{}
	if u := os.Getenv("HTTP_AUTH_USER"); u != "" {
		creds[u] = os.Getenv("HTTP_AUTH_PASS")
	}
	for _, pair := range strings.Split(os.Getenv("HTTP_AUTH_USERS"), ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		u, p, ok := strings.Cut(pair, ":")
		if ok && u != "" {
			creds[u] = p
		}
	}
	if len(creds) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protected := false
		for _, prefix := range protectedPrefixes {
			if strings.HasPrefix(r.URL.Path, prefix) {
				protected = true
				break
			}
		}
		if !protected {
			next.ServeHTTP(w, r)
			return
		}
		u, p, ok := r.BasicAuth()
		if want, found := creds[u]; !ok || !found || p != want {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte("Unauthorized\n"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func respondJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// handleHealth returns a 200 OK response to indicate the server is running.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "error": "method not allowed"})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"success": true, "status": "ok", "service": "ebay-watch",
		"fetch": s.fetchMode, "build_time": s.buildTime,
	})
}

func (s *Server) handleSettingsPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "error": "method not allowed"})
		return
	}
	path := filepath.Join(s.cfg.WebDir, "settings.html")
	http.ServeFile(w, r, path)
}

func (s *Server) handleSearches(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := s.store.ListSearches()
		if err != nil {
			fmt.Printf("[API] /api/searches GET err=%v\n", err)
			respondJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": err.Error()})
			return
		}
		for i := range list {
			list[i].ItemConditions = ebay.ItemConditionKeys(list[i].ItemConditionFilter)
			if yp, yc, yl, err := s.store.GetSearchYearMarketPrices(list[i].ID); err == nil {
				if len(yp) > 0 {
					list[i].MarketPricesByYear = yp
				}
				if len(yc) > 0 {
					list[i].MarketCountsByYear = yc
				}
				if len(yl) > 0 {
					list[i].ListingsByYear = yl
				}
			}
		}
		respondJSON(w, http.StatusOK, map[string]any{"success": true, "searches": list})
	case http.MethodPost:
		var body struct {
			Query          string   `json:"query"`
			ItemConditions []string `json:"item_conditions"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
			respondJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "invalid json"})
			return
		}
		raw := strings.TrimSpace(body.Query)
		if raw == "" {
			respondJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "query required"})
			return
		}
		var displayQuery, condPipe, ebayURL string
		if ebay.LooksLikeEbaySearchURL(raw) {
			var err error
			displayQuery, ebayURL, err = ebay.NormalizeEbaySearchURL(raw, s.cfg.SearchLimit)
			if err != nil {
				respondJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": err.Error()})
				return
			}
		} else {
			displayQuery = raw
			var err error
			condPipe, err = ebay.ItemConditionPipeFromKeys(body.ItemConditions)
			if err != nil {
				respondJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": err.Error()})
				return
			}
		}
		id, err := s.store.AddSearch(displayQuery, condPipe, ebayURL)
		if err != nil {
			fmt.Printf("[API] /api/searches POST err=%v\n", err)
			respondJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": err.Error()})
			return
		}
		fmt.Printf("[API] /api/searches POST query=%q url=%t id=%d\n", displayQuery, ebayURL != "", id)
		respondJSON(w, http.StatusOK, map[string]any{"success": true, "id": id})
	case http.MethodDelete:
		idStr := strings.TrimSpace(r.URL.Query().Get("id"))
		if idStr == "" {
			respondJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "id required"})
			return
		}
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || id <= 0 {
			respondJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "invalid id"})
			return
		}
		if err := s.store.DeleteSearch(id); err != nil {
			fmt.Printf("[API] DELETE /api/searches err=%v\n", err)
			respondJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": err.Error()})
			return
		}
		fmt.Printf("[API] DELETE /api/searches id=%d\n", id)
		respondJSON(w, http.StatusOK, map[string]any{"success": true})
	case http.MethodPatch:
		idStr := strings.TrimSpace(r.URL.Query().Get("id"))
		if idStr == "" {
			respondJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "id required"})
			return
		}
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || id <= 0 {
			respondJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "invalid id"})
			return
		}
		var body struct {
			ShowInResults  *bool     `json:"show_in_results"`
			ItemConditions *[]string `json:"item_conditions"`
			TitleFilter    *string   `json:"title_filter"`
			ExcludeFilter  *string   `json:"exclude_filter"`
			MinPrice       *string   `json:"min_price"`
			MaxPrice       *string   `json:"max_price"`
			Query          *string   `json:"query"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
			respondJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "invalid json"})
			return
		}
		if body.ShowInResults == nil && body.ItemConditions == nil && body.TitleFilter == nil && body.ExcludeFilter == nil && body.MinPrice == nil && body.MaxPrice == nil && body.Query == nil {
			respondJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "no fields to update"})
			return
		}
		if body.ItemConditions != nil {
			sr, err := s.store.GetSearch(id)
			if err != nil {
				if errors.Is(err, store.ErrSearchNotFound) {
					respondJSON(w, http.StatusNotFound, map[string]any{"success": false, "error": err.Error()})
					return
				}
				fmt.Printf("[API] PATCH /api/searches get err=%v\n", err)
				respondJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": err.Error()})
				return
			}
			if strings.TrimSpace(sr.EbaySearchURL) != "" {
				respondJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "item_conditions cannot be changed for URL-based searches"})
				return
			}
			pipe, err := ebay.ItemConditionPipeFromKeys(*body.ItemConditions)
			if err != nil {
				respondJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": err.Error()})
				return
			}
			if err := s.store.SetSearchItemConditionFilter(id, pipe); err != nil {
				if errors.Is(err, store.ErrSearchNotFound) {
					respondJSON(w, http.StatusNotFound, map[string]any{"success": false, "error": err.Error()})
					return
				}
				fmt.Printf("[API] PATCH /api/searches item_conditions err=%v\n", err)
				respondJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": err.Error()})
				return
			}
		}
		if body.ShowInResults != nil {
			if err := s.store.SetSearchShowInResults(id, *body.ShowInResults); err != nil {
				if errors.Is(err, store.ErrSearchNotFound) {
					respondJSON(w, http.StatusNotFound, map[string]any{"success": false, "error": err.Error()})
					return
				}
				fmt.Printf("[API] PATCH /api/searches show_in_results err=%v\n", err)
				respondJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": err.Error()})
				return
			}
		}
		if body.TitleFilter != nil {
			if err := s.store.SetSearchTitleFilter(id, *body.TitleFilter); err != nil {
				if errors.Is(err, store.ErrSearchNotFound) {
					respondJSON(w, http.StatusNotFound, map[string]any{"success": false, "error": err.Error()})
					return
				}
				fmt.Printf("[API] PATCH /api/searches title_filter err=%v\n", err)
				respondJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": err.Error()})
				return
			}
		}
		if body.MinPrice != nil || body.MaxPrice != nil {
			minP := ""
			maxP := ""
			if body.MinPrice != nil {
				minP = *body.MinPrice
			}
			if body.MaxPrice != nil {
				maxP = *body.MaxPrice
			}
			// fetch current values if only one side is being set
			if body.MinPrice == nil || body.MaxPrice == nil {
				sr, err := s.store.GetSearch(id)
				if err != nil {
					if errors.Is(err, store.ErrSearchNotFound) {
						respondJSON(w, http.StatusNotFound, map[string]any{"success": false, "error": err.Error()})
						return
					}
					respondJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": err.Error()})
					return
				}
				if body.MinPrice == nil {
					minP = sr.MinPrice
				}
				if body.MaxPrice == nil {
					maxP = sr.MaxPrice
				}
			}
			if err := s.store.SetSearchPriceRange(id, minP, maxP); err != nil {
				if errors.Is(err, store.ErrSearchNotFound) {
					respondJSON(w, http.StatusNotFound, map[string]any{"success": false, "error": err.Error()})
					return
				}
				fmt.Printf("[API] PATCH /api/searches price_range err=%v\n", err)
				respondJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": err.Error()})
				return
			}
		}
		if body.ExcludeFilter != nil {
			if err := s.store.SetSearchExcludeFilter(id, *body.ExcludeFilter); err != nil {
				if errors.Is(err, store.ErrSearchNotFound) {
					respondJSON(w, http.StatusNotFound, map[string]any{"success": false, "error": err.Error()})
					return
				}
				fmt.Printf("[API] PATCH /api/searches exclude_filter err=%v\n", err)
				respondJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": err.Error()})
				return
			}
		}
		if body.Query != nil {
			q := strings.TrimSpace(*body.Query)
			if q == "" {
				respondJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "query cannot be empty"})
				return
			}
			if err := s.store.SetSearchQuery(id, q); err != nil {
				if errors.Is(err, store.ErrSearchNotFound) {
					respondJSON(w, http.StatusNotFound, map[string]any{"success": false, "error": err.Error()})
					return
				}
				fmt.Printf("[API] PATCH /api/searches query err=%v\n", err)
				respondJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": err.Error()})
				return
			}
		}
		fmt.Printf("[API] PATCH /api/searches id=%d ok\n", id)
		respondJSON(w, http.StatusOK, map[string]any{"success": true})
	default:
		respondJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "error": "method not allowed"})
	}
}

func (s *Server) handleItems(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "error": "method not allowed"})
		return
	}
	list, err := s.store.ListVisibleListings()
	if err != nil {
		fmt.Printf("[API] /api/items GET err=%v\n", err)
		respondJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": err.Error()})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"success": true, "items": list})
}

func (s *Server) handleReject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "error": "method not allowed"})
		return
	}
	var body struct {
		EbayItemID string `json:"ebay_item_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "invalid json"})
		return
	}
	if err := store.ValidateItemID(body.EbayItemID); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": err.Error()})
		return
	}
	// Capture image URLs before Reject() deletes the listing row.
	imgURLs, _ := s.store.GetListingImageURLs(body.EbayItemID)

	if err := s.store.Reject(body.EbayItemID); err != nil {
		fmt.Printf("[API] /api/reject POST err=%v\n", err)
		respondJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": err.Error()})
		return
	}
	fmt.Printf("[API] /api/reject POST ebay_item_id=%s images=%d\n", body.EbayItemID, len(imgURLs))

	// Async: download images and store content hashes so future reposts are auto-rejected.
	if len(imgURLs) > 0 {
		itemID := body.EbayItemID
		urls := imgURLs
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var hashes []string
			for _, u := range urls {
				h, err := imghash.Fetch(ctx, u)
				if err != nil {
					fmt.Printf("[imghash] fetch err item=%s url=%s: %v\n", itemID, u, err)
					continue
				}
				hashes = append(hashes, h)
			}
			if err := s.store.StoreImageHashes(hashes, itemID); err != nil {
				fmt.Printf("[imghash] store err item=%s: %v\n", itemID, err)
			} else {
				fmt.Printf("[imghash] stored %d hashes for item=%s\n", len(hashes), itemID)
			}
		}()
	}

	respondJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (s *Server) handleRejectSeller(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "error": "method not allowed"})
		return
	}
	var body struct {
		SellerName string `json:"seller_name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "invalid json"})
		return
	}
	if err := s.store.RejectSeller(body.SellerName); err != nil {
		fmt.Printf("[API] /api/reject-seller POST err=%v\n", err)
		respondJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": err.Error()})
		return
	}
	fmt.Printf("[API] /api/reject-seller POST seller_name=%s\n", body.SellerName)
	respondJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (s *Server) handleSeen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "error": "method not allowed"})
		return
	}
	var body struct {
		EbayItemID string `json:"ebay_item_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "invalid json"})
		return
	}
	if err := store.ValidateItemID(body.EbayItemID); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": err.Error()})
		return
	}
	if err := s.store.MarkItemSeen(body.EbayItemID); err != nil {
		fmt.Printf("[API] /api/seen POST err=%v\n", err)
		respondJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": err.Error()})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (s *Server) handlePoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "error": "method not allowed"})
		return
	}
	idStr := strings.TrimSpace(r.URL.Query().Get("search_id"))
	if idStr != "" {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || id <= 0 {
			respondJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "invalid search_id"})
			return
		}
		err = poller.RunPollOne(r.Context(), s.store, s.search, id)
		if err != nil {
			if errors.Is(err, store.ErrSearchNotFound) {
				respondJSON(w, http.StatusNotFound, map[string]any{"success": false, "error": err.Error()})
				return
			}
			fmt.Printf("[API] /api/poll search_id=%d err=%v\n", id, err)
			respondJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": err.Error()})
			return
		}
		fmt.Printf("[API] /api/poll search_id=%d ok\n", id)
		respondJSON(w, http.StatusOK, map[string]any{"success": true, "search_id": id})
		return
	}
	if r.URL.Query().Get("async") == "1" {
		s.pollMu.Lock()
		if s.pollActive {
			s.pollMu.Unlock()
			respondJSON(w, http.StatusAccepted, map[string]any{"success": true, "message": "poll already running"})
			return
		}
		s.pollActive = true
		s.pollMu.Unlock()
		go func() {
			defer func() {
				s.pollMu.Lock()
				s.pollActive = false
				s.pollMu.Unlock()
			}()
			if err := poller.RunPollWithMaxAge(context.Background(), s.store, s.search, s.cfg.ListingMaxAge); err != nil {
				fmt.Printf("[API] /api/poll async err=%v\n", err)
			}
		}()
		respondJSON(w, http.StatusAccepted, map[string]any{"success": true, "message": "poll started"})
		return
	}
	err := poller.RunPoll(r.Context(), s.store, s.search)
	if err != nil {
		fmt.Printf("[API] /api/poll err=%v\n", err)
		respondJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": err.Error()})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"success": true})
}

// handleMarketLookup performs an on-demand eBay sold-listings search for a specific item title.
// POST /api/market-lookup  body: {"query":"...", "category":"6001"}
func (s *Server) handleMarketLookup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST only"})
		return
	}
	var req struct {
		Query    string `json:"query"`
		Category string `json:"category"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		respondJSON(w, http.StatusBadRequest, map[string]any{"error": "query required"})
		return
	}
	mp, ok := s.search.(ebay.MarketPricer)
	if !ok {
		respondJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "market lookup unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	result, err := mp.FetchSoldPrices(ctx, req.Query, req.Category)
	if err != nil {
		fmt.Printf("[API] /api/market-lookup err=%v\n", err)
		respondJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"success": true, "result": result})
}

var ogImageRE = regexp.MustCompile(`(?i)<meta[^>]+property=["']og:image["'][^>]+content=["']([^"']+)["']|<meta[^>]+content=["']([^"']+)["'][^>]+property=["']og:image["']`)

// handleItemImage fetches an eBay item page and extracts the og:image URL.
// GET /api/item-image?url=https://www.ebay.com/itm/...
func (s *Server) handleItemImage(w http.ResponseWriter, r *http.Request) {
	itemURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if !strings.HasPrefix(itemURL, "https://www.ebay.com/itm/") {
		respondJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid url"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", itemURL, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		respondJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	// og:image is always in <head> — read only the first 64 KB.
	head := make([]byte, 64*1024)
	n, _ := io.ReadFull(resp.Body, head)
	m := ogImageRE.FindSubmatch(head[:n])
	if m == nil {
		respondJSON(w, http.StatusNotFound, map[string]any{"error": "no image found"})
		return
	}
	imgURL := string(m[1])
	if imgURL == "" {
		imgURL = string(m[2])
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	respondJSON(w, http.StatusOK, map[string]any{"image_url": imgURL})
}
