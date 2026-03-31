package poller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

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
		TitleFilter:   se.TitleFilter,
		ExcludeFilter: se.ExcludeFilter,
		MinPrice:      se.MinPrice,
		MaxPrice:      se.MaxPrice,
	}
	items, err := sch.Search(spec)
	if err != nil {
		fmt.Printf("[POLL] search_id=%d query=%q err=%v\n", se.ID, se.Query, err)
		return fmt.Errorf("search %d: %w", se.ID, err)
	}
	items = applyExcludeFilter(items, spec.ExcludeFilter)
	items = applyTitleFilter(items, spec.TitleFilter)
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

// termMatches returns true when term appears in haystack as a whole word/phrase.
// For single-word alphanumeric terms (e.g. "2E", "EEE") it requires word boundaries
// so "EE" won't match inside "see" or "green". For multi-word phrases (e.g. "Shoe Width: D")
// plain substring match is used since the surrounding context already provides specificity.
func termMatches(haystack, term string) bool {
	if term == "" {
		return false
	}
	// multi-word / contains non-alphanumeric chars → plain substring (already specific)
	if strings.ContainsAny(term, " \t:/-") {
		return strings.Contains(haystack, term)
	}
	// single token: require word boundaries
	re, err := regexp.Compile(`(?i)\b` + regexp.QuoteMeta(term) + `\b`)
	if err != nil {
		return strings.Contains(haystack, term)
	}
	return re.MatchString(haystack)
}

// applyExcludeFilter drops items where any exclude term appears in title+listing_details.
// Terms are comma-separated phrases (not split on whitespace) so multi-word phrases like
// "Shoe Width: D" are matched as a whole.
func applyExcludeFilter(items []ebay.Item, filter string) []ebay.Item {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return items
	}
	var terms []string
	for _, t := range strings.Split(filter, ",") {
		if t = strings.TrimSpace(t); t != "" {
			terms = append(terms, t)
		}
	}
	var keep []ebay.Item
	for _, it := range items {
		haystack := strings.ToLower(it.Title + " " + it.ListingDetails)
		excluded := false
		for _, t := range terms {
			t = strings.ToLower(strings.TrimSpace(t))
			if termMatches(haystack, t) {
				excluded = true
				fmt.Printf("[POLL] excluded item_id=%s matched exclude term %q\n", it.ItemID, t)
				break
			}
		}
		if !excluded {
			keep = append(keep, it)
		}
	}
	return keep
}

// applyTitleFilter drops items whose title contains none of the filter terms.
// filter is a comma-and/or-space-separated list of terms; empty filter passes everything.
func applyTitleFilter(items []ebay.Item, filter string) []ebay.Item {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return items
	}
	// split on commas and whitespace
	terms := strings.FieldsFunc(filter, func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	})
	var keep []ebay.Item
	for _, it := range items {
		haystack := strings.ToLower(it.Title + " " + it.ListingDetails)
		for _, t := range terms {
			t = strings.ToLower(strings.TrimSpace(t))
			if termMatches(haystack, t) {
				keep = append(keep, it)
				break
			}
		}
	}
	return keep
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
