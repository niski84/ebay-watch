package poller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"ebay-watch/internal/ebay"
	"ebay-watch/internal/imghash"
	"ebay-watch/internal/store"
)


// parsePriceDollars extracts the first dollar amount from a price string like "$15,500" or "US $15,500".
// Returns 0 if unparseable.
func parsePriceDollars(s string) int {
	s = strings.ReplaceAll(s, ",", "")
	re := regexp.MustCompile(`\$([\d]+(?:\.\d+)?)`)
	m := re.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	var f float64
	fmt.Sscanf(m[1], "%f", &f)
	return int(f)
}

// medianInts returns the median of a slice of ints (0 if empty).
func medianInts(vals []int) int {
	if len(vals) == 0 {
		return 0
	}
	sorted := make([]int, len(vals))
	copy(sorted, vals)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	mid := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[mid-1] + sorted[mid]) / 2
	}
	return sorted[mid]
}

var yearRE = regexp.MustCompile(`\b(19[6-9]\d|20[0-2]\d)\b`)

// computeYearPrices derives per-year price stats from the active listings already fetched
// by this poll and stores them. No extra network calls needed — the items slice IS the market sample.
func computeYearPrices(st *store.Store, se store.Search, items []ebay.Item) {
	type entry struct {
		prices   []int
		listings []store.SoldListing
	}
	byYear := map[int]*entry{}

	for _, it := range items {
		p := parsePriceDollars(it.PriceValue)
		if p <= 0 {
			continue
		}
		m := yearRE.FindString(it.Title)
		if m == "" {
			continue
		}
		y, _ := strconv.Atoi(m)
		if byYear[y] == nil {
			byYear[y] = &entry{}
		}
		byYear[y].prices = append(byYear[y].prices, p)
		byYear[y].listings = append(byYear[y].listings, store.SoldListing{
			Title: it.Title,
			Price: p,
			URL:   it.ItemWebURL,
		})
	}

	for year, d := range byYear {
		if len(d.prices) < 2 {
			continue
		}
		med := medianInts(d.prices)
		listingsJSON := ""
		if b, err := json.Marshal(d.listings); err == nil {
			listingsJSON = string(b)
		}
		if err := st.SetSearchYearMarketPrice(se.ID, year, med, len(d.prices), listingsJSON); err != nil {
			fmt.Printf("[POLL] year-price store search_id=%d year=%d err=%v\n", se.ID, year, err)
		} else {
			fmt.Printf("[POLL] year-price search_id=%d year=%d median=$%d count=%d\n", se.ID, year, med, len(d.prices))
		}
	}
}

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
	rejectedSellers, err := st.RejectedSellerNames()
	if err != nil {
		return fmt.Errorf("search %d: rejected sellers: %w", se.ID, err)
	}
	rejectedHashes, err := st.RejectedImageHashSet()
	if err != nil {
		fmt.Printf("[POLL] search_id=%d: image hash set err=%v (skipping hash check)\n", se.ID, err)
		rejectedHashes = nil
	}
	var errs []error
	for _, it := range items {
		if _, skip := rejected[it.ItemID]; skip {
			continue
		}
		if it.SellerName != "" {
			if _, skip := rejectedSellers[it.SellerName]; skip {
				continue
			}
		}
		// Image hash check: if we have rejected hashes and the item has an image,
		// download the primary image and compare its content hash.
		if len(rejectedHashes) > 0 && it.ImageURL != "" {
			hashCtx, hashCancel := context.WithTimeout(ctx, 8*time.Second)
			h, hashErr := imghash.Fetch(hashCtx, it.ImageURL)
			hashCancel()
			if hashErr == nil {
				if _, matched := rejectedHashes[h]; matched {
					fmt.Printf("[POLL] auto-reject item_id=%s matched image hash\n", it.ItemID)
					if rejectErr := st.Reject(it.ItemID); rejectErr != nil {
						fmt.Printf("[POLL] auto-reject store err item_id=%s: %v\n", it.ItemID, rejectErr)
					}
					rejected[it.ItemID] = struct{}{} // prevent re-processing in same poll
					continue
				}
			}
		}
		galleryJSON := ""
		if len(it.ImageURLs) > 0 {
			if b, err := json.Marshal(it.ImageURLs); err == nil {
				galleryJSON = string(b)
			}
		}
		if err := st.UpsertListing(se.ID, it.ItemID, it.Title, it.PriceValue, it.PriceCurrency, it.ImageURL, galleryJSON, it.ItemWebURL, it.Condition, it.ListingDetails, it.SellerName, it.SellerFeedback); err != nil {
			fmt.Printf("[POLL] upsert search_id=%d item_id=%s err=%v\n", se.ID, it.ItemID, err)
			errs = append(errs, err)
		}
	}
	if err := st.MarkSearchPolled(se.ID); err != nil {
		errs = append(errs, err)
	}
	fmt.Printf("[POLL] search_id=%d query=%q items=%d\n", se.ID, se.Query, len(items))
	computeYearPrices(st, se, items)
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
// A 20-second delay between searches lets Firefox fully exit, frees memory, and
// avoids eBay rate-limiting consecutive headless requests from the same IP.
func RunPoll(ctx context.Context, st *store.Store, search ebay.Searcher) error {
	return RunPollWithMaxAge(ctx, st, search, 21*24*time.Hour)
}

// RunPollWithMaxAge is like RunPoll but also purges listings older than maxAge
// from actively-polled searches, and hard-purges everything older than 90 days.
func RunPollWithMaxAge(ctx context.Context, st *store.Store, search ebay.Searcher, maxAge time.Duration) error {
	searches, err := st.ListEnabledSearches()
	if err != nil {
		return err
	}
	var errs []error
	for i, se := range searches {
		if i > 0 {
			select {
			case <-ctx.Done():
				return errors.Join(append(errs, ctx.Err())...)
			case <-time.After(60 * time.Second):
			}
		}
		if err := pollSearch(ctx, st, search, se); err != nil {
			errs = append(errs, err)
		}
	}
	if n, err := st.PurgeStaleListings(maxAge, 90*24*time.Hour); err != nil {
		fmt.Printf("[POLL] purge error: %v\n", err)
	} else if n > 0 {
		fmt.Printf("[POLL] purged %d stale listings\n", n)
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
func StartBackground(ctx context.Context, interval time.Duration, maxAge time.Duration, st *store.Store, search ebay.Searcher) {
	if interval <= 0 {
		interval = 4 * time.Hour
	}
	if maxAge <= 0 {
		maxAge = 21 * 24 * time.Hour
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	run := func() {
		if err := RunPollWithMaxAge(context.Background(), st, search, maxAge); err != nil {
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
