package searchesmd

import (
	"bufio"
	"bytes"
	"strings"
)

// ParseQueries extracts bullet lines under "## Active searches" (markdown).
func ParseQueries(data []byte) []string {
	var out []string
	inSection := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(strings.ToLower(line), "## ") {
			inSection = strings.EqualFold(strings.TrimPrefix(line, "## "), "active searches")
			continue
		}
		if !inSection {
			continue
		}
		if strings.HasPrefix(line, "## ") {
			break
		}
		if strings.HasPrefix(line, "- ") {
			q := strings.TrimSpace(strings.TrimPrefix(line, "- "))
			if q != "" {
				out = append(out, q)
			}
		}
	}
	return out
}
