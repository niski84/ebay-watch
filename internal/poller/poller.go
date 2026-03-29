package poller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"ebay-watch/internal/ebay"
	"ebay-watch/internal/store"
)

func pollSearch(ctx context.Context, st *store.Store, sch ebay.Searcher, se store.Search) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	spec := ebay.SearchSpec{
		Keywords:      se.Query,
		ItemCondition: se.ItemConditionFilter,
		EbayURL:       se.EbaySearchURL,
	}
	items, err := sch.Search(spec)
	if err != nil {
		fmt.Printf("[POLL] search_id=%d query=%q err=%v\n", se.ID, se.Query, err)
		return fmt.Errorf("search %d: %w", se.ID, err)
	}
	rejected, err := st.RejectedEbayItemIDs()
	if err != nil {
		return fmt.Errorf("search %d: rejected ids: %w", se.ID, err)
	}
	var errs []error
	for _, it := range items {
		if _, skip := rejected[it.ItemID]; skip {
			continue
		}
		galleryJSON := ""
		if len(it.ImageURLs) > 0 {
			if b, err := json.Marshal(it.ImageURLs); err == nil {
				galleryJSON = string(b)
			}
		}
		if err := st.UpsertListing(se.ID, it.ItemID, it.Title, it.PriceValue, it.PriceCurrency, it.ImageURL, galleryJSON, it.ItemWebURL, it.Condition, it.ListingDetails); err != nil {
			fmt.Printf("[POLL] upsert search_id=%d item_id=%s err=%v\n", se.ID, it.ItemID, err)
			errs = append(errs, err)
		}
	}
	if err := st.MarkSearchPolled(se.ID); err != nil {
		errs = append(errs, err)
	}
	fmt.Printf("[POLL] search_id=%d query=%q items=%d\n", se.ID, se.Query, len(items))
	return errors.Join(errs...)
}

// RunPoll loads enabled searches, queries eBay for each, and upserts listing rows.
func RunPoll(ctx context.Context, st *store.Store, search ebay.Searcher) error {
	searches, err := st.ListEnabledSearches()
	if err != nil {
		return err
	}
	var errs []error
	for _, se := range searches {
		if err := pollSearch(ctx, st, search, se); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// RunPollOne runs Playwright + upsert for a single saved search by id (any row, enabled or not).
func RunPollOne(ctx context.Context, st *store.Store, search ebay.Searcher, searchID int64) error {
	se, err := st.GetSearch(searchID)
	if err != nil {
		return err
	}
	return pollSearch(ctx, st, search, *se)
}

// StartBackground runs RunPoll on a ticker until ctx is done.
func StartBackground(ctx context.Context, interval time.Duration, st *store.Store, search ebay.Searcher) {
	if interval <= 0 {
		interval = 4 * time.Hour
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	run := func() {
		if err := RunPoll(context.Background(), st, search); err != nil {
			fmt.Printf("[POLL] cycle error: %v\n", err)
		}
	}
	run()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}
