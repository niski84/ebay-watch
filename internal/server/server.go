package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"ebay-watch/internal/config"
	"ebay-watch/internal/ebay"
	"ebay-watch/internal/poller"
	"ebay-watch/internal/store"
)

// Server wires HTTP handlers to the store and listing fetcher.
type Server struct {
	cfg       config.Config
	store     *store.Store
	search    ebay.Searcher
	fetchMode string
}

// New creates an HTTP server facade.
func New(cfg config.Config, st *store.Store, search ebay.Searcher, fetchMode string) *Server {
	return &Server{cfg: cfg, store: st, search: search, fetchMode: fetchMode}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/searches", s.handleSearches)
	mux.HandleFunc("/api/items", s.handleItems)
	mux.HandleFunc("/api/reject", s.handleReject)
	mux.HandleFunc("/api/seen", s.handleSeen)
	mux.HandleFunc("/api/poll", s.handlePoll)
	mux.HandleFunc("/settings", s.handleSettingsPage)

	fs := http.FileServer(http.Dir(s.cfg.WebDir))
	mux.Handle("/", fs)
	return logRequests(basicAuth(mux))
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
func basicAuth(next http.Handler) http.Handler {
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
		u, p, ok := r.BasicAuth()
		if want, found := creds[u]; !ok || !found || p != want {
			w.Header().Set("WWW-Authenticate", `Basic realm="ebay-watch"`)
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
		"fetch": s.fetchMode,
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
	if err := s.store.Reject(body.EbayItemID); err != nil {
		fmt.Printf("[API] /api/reject POST err=%v\n", err)
		respondJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": err.Error()})
		return
	}
	fmt.Printf("[API] /api/reject POST ebay_item_id=%s\n", body.EbayItemID)
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
	err := poller.RunPoll(r.Context(), s.store, s.search)
	if err != nil {
		fmt.Printf("[API] /api/poll err=%v\n", err)
		respondJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": err.Error()})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"success": true})
}
