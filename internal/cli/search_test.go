package cli

import (
	"testing"

	"github.com/orshih6/uneguictl/internal/unegui"
)

func TestParsePrice(t *testing.T) {
	cases := map[string]int64{
		"":          0,
		"1500000":   1500000,
		"1,500,000": 1500000,
		"1.5m":      1500000,
		"1.5M":      1500000,
		"500k":      500000,
		"2.5 сая":   2500000,
		"300 мянга": 300000,
		"20000₮":    20000,
	}
	for in, want := range cases {
		got, err := parsePrice(in)
		if err != nil || got != want {
			t.Errorf("parsePrice(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"cheap", "-5", "1.5x"} {
		if _, err := parsePrice(in); err == nil {
			t.Errorf("parsePrice(%q) should fail", in)
		}
	}
}

func TestContainsWord(t *testing.T) {
	cases := []struct {
		s, w string
		want bool
	}{
		{"apple iphone 15 pro, 256 gb", "15", true},
		{"apple iphone 15 pro, 256 gb", "pro", true},
		{"apple iphone 150", "15", false},
		{"hp probook 450", "pro", false},
		{"toyota prius 30", "prius", true},
		{"тоёота приус 30", "приус", true},
		{"тоёота приусс", "приус", false},
		{"iphone", "iphone", true},
		{"", "x", false},
	}
	for _, c := range cases {
		if got := containsWord(c.s, c.w); got != c.want {
			t.Errorf("containsWord(%q, %q) = %v", c.s, c.w, got)
		}
	}
}

func TestItemFilter(t *testing.T) {
	items := []unegui.Item{
		{ID: 1, Title: "Apple iPhone 15 Pro, 256 gb", Promoted: "VIP"},
		{ID: 2, Title: "Apple iPhone 15 Pro Max"},
		{ID: 3, Title: "Принтерийн хор 15"},
	}
	keep := itemFilter("iPhone 15 pro", searchOptions{match: true, noPromoted: true})
	var got []int64
	for _, it := range items {
		if keep(it) {
			got = append(got, it.ID)
		}
	}
	if len(got) != 1 || got[0] != 2 {
		t.Errorf("kept %v, want [2]", got)
	}
}

func TestSortByPrice(t *testing.T) {
	items := []unegui.Item{
		{ID: 1, Price: 1700000, Promoted: "VIP"},
		{ID: 2, Price: 0},
		{ID: 3, Price: 300000, Promoted: "Онцгой"},
		{ID: 4, Price: 100000},
		{ID: 5, Price: 300000},
	}
	sortByPrice(items, false)
	want := []int64{4, 3, 5, 1, 2}
	for i, id := range want {
		if items[i].ID != id {
			t.Fatalf("ascending order = %v, want %v", ids(items), want)
		}
	}
	sortByPrice(items, true)
	if got := ids(items); got[0] != 1 || got[len(got)-1] != 2 {
		t.Errorf("descending order = %v; want 1 first and the priceless 2 last", got)
	}
}

func ids(items []unegui.Item) []int64 {
	out := make([]int64, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

func TestParseFilters(t *testing.T) {
	v, err := parseFilters([]string{"attrs__bagtaamzh=7,12", "attrs__year_min = 2018"})
	if err != nil {
		t.Fatal(err)
	}
	if len(v["attrs__bagtaamzh"]) != 2 || v.Get("attrs__year_min") != "2018" {
		t.Errorf("filters = %v", v)
	}
	for _, bad := range []string{"novalue", "=7", "slug="} {
		if _, err := parseFilters([]string{bad}); err == nil {
			t.Errorf("parseFilters(%q) should fail", bad)
		}
	}
}

func TestOrdering(t *testing.T) {
	if o, _ := ordering("", ""); o != "newest" {
		t.Errorf("no words: %q", o)
	}
	if o, _ := ordering("", "x"); o != "relevance" {
		t.Errorf("words: %q", o)
	}
	if o, _ := ordering("Cheapest", "x"); o != "cheapest" {
		t.Errorf("case: %q", o)
	}
	if _, err := ordering("price", "x"); err == nil {
		t.Error("unknown sort should fail")
	}
}
