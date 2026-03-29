package ebay

import (
	"strings"
	"testing"
)

func TestNormalizeEbaySearchURL_ShoeExample(t *testing.T) {
	raw := "https://www.ebay.com/sch/i.html?_nkw=brooks+max+Men%27s+SIZE+10+Extra+Wide&_sacat=0&_from=R40&US%2520Shoe%2520Size=US%2520Men%252010&US%2520Shoe%2520Size=10&rt=nc&LH_ItemCondition=3&Shoe%2520Width=EEEE%7CEE&_dcat=15709"
	label, out, err := NormalizeEbaySearchURL(raw, 48)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(label, "brooks") {
		t.Fatalf("label: %q", label)
	}
	if strings.Contains(out, "_from=") {
		t.Fatalf("should drop _from: %s", out)
	}
	if !strings.Contains(out, "LH_ItemCondition=3") {
		t.Fatalf("missing condition: %s", out)
	}
	if !strings.Contains(out, "_ipg=48") {
		t.Fatalf("missing _ipg: %s", out)
	}
	if !strings.Contains(out, "_dcat=15709") {
		t.Fatalf("missing category: %s", out)
	}
	if !strings.Contains(out, "Shoe") || !strings.Contains(out, "Width") {
		t.Fatalf("missing shoe width facet: %s", out)
	}
	if !strings.Contains(label, "width:") || !strings.Contains(strings.ToUpper(label), "EE") {
		t.Fatalf("display label should include width facet for client filter: %q", label)
	}
}
