package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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

	fs := http.FileServer(http.Dir(s.cfg.WebDir))
	mux.Handle("/", fs)
	return logRequests(mux)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Printf("[API] %s %s\n", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func respondJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

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
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
			respondJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "invalid json"})
			return
		}
		if body.ShowInResults == nil && body.ItemConditions == nil {
			respondJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "show_in_results or item_conditions required"})
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
