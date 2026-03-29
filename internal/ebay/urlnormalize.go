package ebay

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Query params eBay adds for tracking or UI that we strip when saving a pasted SERP URL.
var ebaySearchParamBlacklist = map[string]struct{}{
	"_from":    {},
	"_trk":     {},
	"_trksid":  {},
	"_tab":     {},
	"mkcid":    {},
	"mkrid":    {},
	"toolid":   {},
	"campid":   {},
	"customid": {},
	"_uid":     {},
	"hash":     {},
	"_ssn":     {},
}

// LooksLikeEbaySearchURL reports whether the user input should be parsed as an eBay URL.
func LooksLikeEbaySearchURL(s string) bool {
	t := strings.TrimSpace(strings.ToLower(s))
	if strings.HasPrefix(t, "http://") || strings.HasPrefix(t, "https://") {
		return strings.Contains(t, "ebay.")
	}
	return strings.Contains(t, "ebay.com/sch") || strings.Contains(t, "ebay.co.uk/sch")
}

// MaybePrependHTTPS adds a scheme when the user pasted a host/path without it.
func MaybePrependHTTPS(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	l := strings.ToLower(s)
	if strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") {
		return s
	}
	if strings.Contains(l, "ebay.com/") || strings.Contains(l, "ebay.co.uk/") {
		return "https://" + strings.TrimPrefix(strings.TrimPrefix(s, "http://"), "//")
	}
	return s
}

func deepQueryUnescape(s string) string {
	prev := s
	for i := 0; i < 8; i++ {
		u, err := url.QueryUnescape(prev)
		if err != nil || u == prev {
			break
		}
		prev = u
	}
	return prev
}

func isBlacklistedParam(key string) bool {
	k := strings.TrimSpace(strings.ToLower(key))
	_, ok := ebaySearchParamBlacklist[k]
	return ok
}

func normalizeEbayHost(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "ebay.com" || strings.HasSuffix(h, ".ebay.com") {
		return "www.ebay.com"
	}
	if h == "ebay.co.uk" || strings.HasSuffix(h, ".ebay.co.uk") {
		return "www.ebay.co.uk"
	}
	return host
}

// NormalizeEbaySearchURL parses a pasted eBay search URL, drops noisy params, normalizes host,
// and sets _ipg to pageLimit. Returns a short display label (usually decoded _nkw) and the final https URL.
func NormalizeEbaySearchURL(raw string, pageLimit int) (displayLabel string, fullURL string, err error) {
	raw = MaybePrependHTTPS(strings.TrimSpace(raw))
	if raw == "" {
		return "", "", fmt.Errorf("empty URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("parse URL: %w", err)
	}
	if !strings.Contains(strings.ToLower(u.Host), "ebay.") {
		return "", "", fmt.Errorf("not an eBay URL")
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/sch/") {
		return "", "", fmt.Errorf("expected an eBay search URL path under /sch/ (got %q)", path)
	}

	values, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", "", fmt.Errorf("parse query: %w", err)
	}

	out := make(url.Values)
	for k, vals := range values {
		k2 := deepQueryUnescape(k)
		if k2 == "" || isBlacklistedParam(k2) {
			continue
		}
		for _, v := range vals {
			out.Add(k2, deepQueryUnescape(v))
		}
	}

	if pageLimit > 0 {
		out.Set("_ipg", strconv.Itoa(pageLimit))
	}

	var keys []string
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var pairs []string
	for _, k := range keys {
		for _, v := range out[k] {
			pairs = append(pairs, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	rawQuery := strings.Join(pairs, "&")

	u2 := url.URL{
		Scheme:   "https",
		Host:     normalizeEbayHost(u.Host),
		Path:     path,
		RawQuery: rawQuery,
	}
	fullURL = u2.String()

	displayLabel = strings.TrimSpace(out.Get("_nkw"))
	if displayLabel == "" {
		displayLabel = "eBay search (URL)"
	}
	displayLabel = appendShoeWidthFacetToDisplay(displayLabel, out)
	return displayLabel, fullURL, nil
}

// appendShoeWidthFacetToDisplay appends shoe-width facet values from the URL so the saved label
// still reflects width intent when filtering listings (eBay SERP often includes D-width items).
func appendShoeWidthFacetToDisplay(display string, out url.Values) string {
	var parts []string
	for k, vals := range out {
		if len(vals) == 0 {
			continue
		}
		kl := strings.ToLower(strings.TrimSpace(k))
		if kl == "shoe width" || (strings.Contains(kl, "shoe") && strings.Contains(kl, "width")) {
			for _, v := range vals {
				v = strings.TrimSpace(v)
				if v != "" {
					parts = append(parts, v)
				}
			}
		}
	}
	if len(parts) == 0 {
		return display
	}
	sort.Strings(parts)
	suffix := strings.Join(parts, " · ")
	display = strings.TrimSpace(display)
	if display == "" {
		return "width: " + suffix
	}
	return display + " · width: " + suffix
}
