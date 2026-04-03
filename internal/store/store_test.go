package store

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSearchJSONFields(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	s := Search{
		ID:                  1,
		Query:               "macbook pro",
		Enabled:             true,
		ShowInResults:       true,
		ItemConditionFilter: "3",
		EbaySearchURL:       "https://www.ebay.com/sch/i.html?_nkw=macbook+pro",
		TitleFilter:         "pro",
		ExcludeFilter:       "broken",
		MinPrice:            "100",
		MaxPrice:            "500",
		CreatedAt:           now,
	}

	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}

	for _, field := range []string{"id", "query", "enabled", "show_in_results",
		"item_condition_filter", "ebay_search_url", "title_filter",
		"exclude_filter", "min_price", "max_price", "created_at"} {
		if _, ok := got[field]; !ok {
			t.Errorf("missing JSON field %q", field)
		}
	}
}

func TestListingJSONFields(t *testing.T) {
	l := Listing{
		EbayItemID:     "123456789",
		SearchID:       1,
		SearchQuery:    "macbook pro",
		Title:          "Apple MacBook Pro 14",
		Condition:      "Used",
		ListingDetails: "Good condition",
		PriceValue:     "299.99",
		PriceCurrency:  "USD",
		ImageURL:       "https://i.ebayimg.com/images/g/abc/s-l500.jpg",
		ImageURLs:      []string{"https://i.ebayimg.com/images/g/abc/s-l500.jpg"},
		ItemWebURL:     "https://www.ebay.com/itm/123456789",
		FetchedAt:      time.Now().UTC(),
		Seen:           false,
	}

	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}

	for _, field := range []string{"ebay_item_id", "search_id", "search_query",
		"title", "condition", "listing_details", "price_value", "price_currency",
		"image_url", "item_web_url", "fetched_at"} {
		if _, ok := got[field]; !ok {
			t.Errorf("missing JSON field %q", field)
		}
	}
}
