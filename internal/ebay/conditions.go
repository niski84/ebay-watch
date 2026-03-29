package ebay

import (
	"fmt"
	"sort"
	"strings"
)

// UI / API keys for www.eBay.com LH_ItemCondition (numeric codes).
const (
	ItemCondKeyNew           = "new"
	ItemCondKeyUsedExcellent = "used_excellent" // eBay label "Very Good"
	ItemCondKeyUsedGood      = "used_good"
)

var itemCondKeyToCodes = map[string][]string{
	ItemCondKeyNew:           {"1000", "1500", "1750"}, // New, New other, New with defects
	ItemCondKeyUsedExcellent: {"4000"},                 // Very Good
	ItemCondKeyUsedGood:      {"5000"},                 // Good
}

var itemCondCodeToKey = func() map[string]string {
	m := make(map[string]string)
	for k, codes := range itemCondKeyToCodes {
		for _, c := range codes {
			m[c] = k
		}
	}
	return m
}()

// ItemConditionKeys maps a stored LH_ItemCondition pipe string to canonical preset keys.
func ItemConditionKeys(pipe string) []string {
	pipe = strings.TrimSpace(pipe)
	if pipe == "" {
		return nil
	}
	seen := map[string]bool{}
	var keys []string
	for _, part := range strings.Split(pipe, "|") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if k := itemCondCodeToKey[part]; k != "" && !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// ItemConditionPipeFromKeys builds the LH_ItemCondition query value from preset keys (| separated).
func ItemConditionPipeFromKeys(keys []string) (string, error) {
	seenCode := map[string]bool{}
	var codes []string
	for _, raw := range keys {
		k := strings.TrimSpace(strings.ToLower(raw))
		if k == "" {
			continue
		}
		list, ok := itemCondKeyToCodes[k]
		if !ok {
			return "", fmt.Errorf("unknown item_conditions key %q", raw)
		}
		for _, c := range list {
			if !seenCode[c] {
				seenCode[c] = true
				codes = append(codes, c)
			}
		}
	}
	if len(codes) == 0 {
		return "", nil
	}
	sort.Strings(codes)
	return strings.Join(codes, "|"), nil
}
