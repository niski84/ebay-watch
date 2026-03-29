package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"ebay-watch/internal/config"
	"ebay-watch/internal/ebay"
	"ebay-watch/internal/poller"
	"ebay-watch/internal/searchesmd"
	"ebay-watch/internal/server"
	"ebay-watch/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	dbPath := filepath.Join(cfg.DataDir, "state.db")
	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	if err := bootstrapSearches(cfg, st); err != nil {
		log.Fatalf("bootstrap: %v", err)
	}

	searcher := ebay.NewPlaywright(
		cfg.PlaywrightScript,
		cfg.NodePath,
		cfg.SearchLimit,
		cfg.PlaywrightTimeout,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go poller.StartBackground(ctx, cfg.PollInterval, st, searcher)

	srv := server.New(cfg, st, searcher, "ebay.com")
	addr := ":" + cfg.Port
	fmt.Printf("[BOOT] ebay-watch listening on %s web=%s data=%s poll=%s fetch=ebay.com (Playwright)\n", addr, cfg.WebDir, cfg.DataDir, cfg.PollInterval)
	log.Fatal(http.ListenAndServe(addr, srv.Routes()))
}

func bootstrapSearches(cfg config.Config, st *store.Store) error {
	n, err := st.TotalSearchRows()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	data, err := os.ReadFile(cfg.SearchesMDPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("[BOOT] no %s yet; add searches via UI or create that file\n", cfg.SearchesMDPath)
			return nil
		}
		return err
	}
	queries := searchesmd.ParseQueries(data)
	if len(queries) == 0 {
		return nil
	}
	if err := st.SeedSearches(queries); err != nil {
		return err
	}
	fmt.Printf("[BOOT] seeded %d search(es) from %s\n", len(queries), cfg.SearchesMDPath)
	return nil
}
