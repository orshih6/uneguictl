package unegui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func readFixture(t *testing.T, name string, v any) {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

func TestListingPage(t *testing.T) {
	var raw rawListing
	readFixture(t, "listing.json", &raw)
	p := raw.page("utas--dugaar/gar-utas-ba-utasnuud/iphone")

	if p.Count != 998 || len(p.Items) != 3 || !p.More() {
		t.Fatalf("count %d, %d items, more %v", p.Count, len(p.Items), p.More())
	}
	it := p.Items[0]
	if it.ID != 10828312 || it.Price != 1700000 || it.Currency != "MNT" {
		t.Errorf("item = %+v", it)
	}
	if it.Slug != "10828312_apple-iphone-15-pro-256-gb-natural-titanium" {
		t.Errorf("slug = %q", it.Slug)
	}
	if !strings.HasPrefix(it.URL, "https://www.unegui.mn/adv/10828312_") {
		t.Errorf("url = %q", it.URL)
	}
	if it.Promoted != "VIP" {
		t.Errorf("promoted = %q", it.Promoted)
	}
	if got := strings.Join(it.Category, "/"); got != "Утас, дугаар/Гар утас ба утаснууд/iPhone" {
		t.Errorf("category = %q", got)
	}
	if !it.Seller.Verified {
		t.Error("an e-Mongolia verified seller should count as verified")
	}
	for _, s := range p.Subcategories {
		if strings.ContainsAny(s.Path, "?") || strings.HasSuffix(s.Path, "/") {
			t.Errorf("subcategory path %q still carries the query or slash", s.Path)
		}
	}
}

func TestLastPageHasNoMore(t *testing.T) {
	next := "?cursor=1"
	raw := rawListing{NextPage: &next}
	if raw.page("").More() {
		t.Error("a page with no adverts must end the walk even if a cursor is given")
	}
}

func TestCardAdvert(t *testing.T) {
	var raw rawCard
	readFixture(t, "card.json", &raw)
	a := raw.advert()
	if a.ID != 10828312 || a.Price != 1700000 || a.PriceNote != "Үнэ тохирно" {
		t.Errorf("advert = %+v", a)
	}
	if a.URL != "https://www.unegui.mn/adv/10828312_apple-iphone-15-pro-256-gb-natural-titanium/" {
		t.Errorf("url = %q", a.URL)
	}
	if strings.Contains(a.Description, "\r") {
		t.Error("description keeps CRLF")
	}
	if len(a.Features) == 0 || len(a.Images) == 0 || len(a.Category) != 4 {
		t.Errorf("features %d, images %d, category %v", len(a.Features), len(a.Images), a.Category)
	}
}

func TestAdvertSlug(t *testing.T) {
	const want = "10828312_apple-iphone-15-pro"
	for _, in := range []string{
		"https://www.unegui.mn/adv/10828312_apple-iphone-15-pro/",
		"https://m.unegui.mn/adv/10828312_apple-iphone-15-pro/?utm=x",
		"/adv/10828312_apple-iphone-15-pro/",
		" 10828312_apple-iphone-15-pro ",
	} {
		got, err := AdvertSlug(in)
		if err != nil || got != want {
			t.Errorf("AdvertSlug(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"10828312", "https://example.com/adv/1_x/", "iphone", ""} {
		if got, err := AdvertSlug(in); err == nil {
			t.Errorf("AdvertSlug(%q) = %q, want an error", in, got)
		}
	}
}

func TestCleanCategory(t *testing.T) {
	cases := map[string]string{
		"":                                        "",
		"utas--dugaar/iphone":                     "utas--dugaar/iphone",
		"/utas--dugaar/iphone/":                   "utas--dugaar/iphone",
		"https://www.unegui.mn/avto-mashin/?q=x":  "avto-mashin",
		"https://www.unegui.mn/search/?q=iphone":  "",
		"utas--dugaar/gar-utas/samsung/?q=iphone": "utas--dugaar/gar-utas/samsung",
	}
	for in, want := range cases {
		got, err := cleanCategory(in)
		if err != nil || got != want {
			t.Errorf("cleanCategory(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"https://www.unegui.mn/adv/1_x/", "https://evil.example/avto-mashin/"} {
		if _, err := cleanCategory(in); err == nil {
			t.Errorf("cleanCategory(%q) should fail", in)
		}
	}
}

func TestQueryParams(t *testing.T) {
	q := Query{Text: "iphone 15", Ordering: "cheapest", PriceMin: 1000, PriceMax: 2000,
		Filters: map[string][]string{"attrs__bagtaamzh": {"7", "12"}}, PageSize: 500}
	v := q.params()
	if v.Get("q") != "iphone 15" || v.Get("price_min") != "1000" || v.Get("price_max") != "2000" ||
		v.Get("ordering") != "cheapest" || len(v["attrs__bagtaamzh"]) != 2 {
		t.Errorf("params = %v", v)
	}
	if v.Get("page_size") != "100" {
		t.Errorf("page_size = %q, want it capped at %d", v.Get("page_size"), MaxPageSize)
	}
	if v := (Query{}).params(); v.Has("q") || v.Has("price_min") || v.Has("ordering") {
		t.Errorf("zero query sends %v", v)
	}
}

// testClient points a Client at a local server; tls-client speaks plain HTTP
// to it.
func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	c.base = srv.URL
	return c
}

func TestUnknownCategoryIsAnError(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"count": 5, "rubric": {}, "adverts": [], "next_page": null}`))
	})
	_, err := c.Search(context.Background(), Query{Category: "no-such"})
	if !errors.Is(err, ErrNoCategory) {
		t.Errorf("err = %v, want ErrNoCategory", err)
	}
	if _, err := c.Search(context.Background(), Query{Text: "x"}); err != nil {
		t.Errorf("a site-wide search has no rubric and is fine: %v", err)
	}
}

func TestStatusMapping(t *testing.T) {
	cases := []struct {
		status int
		ctype  string
		want   error
	}{
		{403, "text/html", ErrBlocked},
		{429, "text/html", ErrBlocked},
		{404, "application/json", ErrNotFound},
	}
	for _, tc := range cases {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", tc.ctype)
			w.WriteHeader(tc.status)
			w.Write([]byte("<html>Just a moment...</html>"))
		})
		_, err := c.Advert(context.Background(), "1_x")
		if !errors.Is(err, tc.want) {
			t.Errorf("status %d: err = %v, want %v", tc.status, err, tc.want)
		}
	}
}

func TestRequestPaths(t *testing.T) {
	var paths []string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.RawQuery, "cursor=") {
			w.Write([]byte(`{"count": 2, "rubric": {"id": 1}, "adverts": [{"id": 2, "url": "/adv/2_b/"}], "next_page": null}`))
			return
		}
		w.Write([]byte(`{"count": 2, "rubric": {"id": 1}, "adverts": [{"id": 1, "url": "/adv/1_a/"}], "next_page": "?cursor=1&q=x"}`))
	})
	ctx := context.Background()
	p, err := c.Search(ctx, Query{Category: "/avto-mashin/", Text: "x", PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	p, err = c.Next(ctx, p)
	if err != nil || p == nil || p.Items[0].ID != 2 || p.More() {
		t.Fatalf("second page = %+v, %v", p, err)
	}
	if p, err := c.Next(ctx, p); p != nil || err != nil {
		t.Errorf("Next after the last page = %v, %v", p, err)
	}
	want := []string{
		"/api/v2/spa/adverts/listing/avto-mashin/?page_size=1&q=x",
		"/api/v2/spa/adverts/listing/avto-mashin/?cursor=1&q=x",
	}
	if strings.Join(paths, "\n") != strings.Join(want, "\n") {
		t.Errorf("paths =\n%s\nwant\n%s", strings.Join(paths, "\n"), strings.Join(want, "\n"))
	}
}
