package unegui

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const listingAPI = "/api/v2/spa/adverts/listing/"

// MaxPageSize is the most adverts the API returns in one page; it answers
// 422 above it.
const MaxPageSize = 100

// Orderings the API accepts. Promoted adverts (VIP, then "Онцгой") always
// come before the rest whichever is chosen, so "cheapest" is cheapest within
// each tier, not overall.
var Orderings = []string{"relevance", "newest", "cheapest", "expensive"}

// Query is one search. Every field is optional; the zero Query lists the
// newest adverts on the whole site.
type Query struct {
	// Category is a category path as it appears in the site's URLs, e.g.
	// "utas--dugaar/gar-utas-ba-utasnuud/iphone". Empty searches everywhere.
	Category string
	// Text is the free-text search, the site's "q".
	Text     string
	Ordering string
	PriceMin int64
	PriceMax int64
	// Filters are a category's extra filters, by slug (see Client.Filters),
	// e.g. "attrs__bagtaamzh" = ["7"]. Range filters take slug_min / slug_max.
	Filters  url.Values
	PageSize int
}

// Page is one page of results.
type Page struct {
	// Count is how many adverts match in all, as the site reports it.
	Count int
	Title string
	Items []Item
	// Subcategories narrow the current results, with how many match in each.
	Subcategories []Subcategory
	// Category is the category path searched, "" for the whole site.
	Category string
	// next is the API's cursor for the following page, "" on the last.
	next string
}

// More reports whether another page follows.
func (p *Page) More() bool { return p.next != "" }

// Subcategory is a narrower category with the number of matching adverts.
type Subcategory struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Path  string `json:"path"`
	Count int    `json:"count"`
}

// Item is one advert as a listing shows it.
type Item struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	// Price is in Currency's units, 0 when the advert names none.
	Price     int64  `json:"price"`
	Currency  string `json:"currency"`
	PriceText string `json:"price_text"`
	// PriceNote is e.g. "Үнэ тохирно" (price negotiable).
	PriceNote string `json:"price_note,omitempty"`
	Location  string `json:"location"`
	// Published is relative and in Mongolian, e.g. "2 өдрийн өмнө".
	Published string `json:"published"`
	URL       string `json:"url"`
	// Slug is the "<id>_<name>" key Client.Advert takes.
	Slug string `json:"slug"`
	// Promoted is "VIP" or "Онцгой" for paid placement, "" otherwise.
	Promoted string   `json:"promoted,omitempty"`
	Category []string `json:"category"`
	Labels   []string `json:"labels,omitempty"`
	Seller   Seller   `json:"seller"`
	Images   []string `json:"images,omitempty"`
}

// Seller is who posted an advert.
type Seller struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Business bool   `json:"business"`
	// Verified is true when the seller confirmed their identity with
	// e-Mongolia or the site.
	Verified bool `json:"verified"`
}

type rawListing struct {
	NextPage     *string       `json:"next_page"`
	Count        int           `json:"count"`
	Title        string        `json:"title"`
	ChildRubrics []Subcategory `json:"child_rubrics"`
	Adverts      []rawAdvert   `json:"adverts"`
	Rubric       struct {
		ID int `json:"id"`
	} `json:"rubric"`
}

type rawAdvert struct {
	ID               int64    `json:"id"`
	Title            string   `json:"title"`
	Price            string   `json:"price"`
	PriceDescription string   `json:"price_description"`
	Currency         string   `json:"currency"`
	PriceRaw         string   `json:"price_without_currency"`
	Published        string   `json:"published"`
	URL              string   `json:"url"`
	Location         string   `json:"location"`
	Images           []string `json:"images"`
	Labels           []struct {
		Text string `json:"text"`
	} `json:"labels"`
	AdType *struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"ad_type"`
	Rubric struct {
		Slug      string `json:"slug"`
		Ancestors []struct {
			Name string `json:"name"`
		} `json:"ancestors"`
	} `json:"rubric"`
	User struct {
		ID                int64  `json:"id"`
		Name              string `json:"name"`
		Verified          bool   `json:"verified"`
		EMongoliaVerified bool   `json:"emongolia_verified"`
		IsBusiness        bool   `json:"is_bussiness"`
	} `json:"user"`
}

// Search fetches the first page of q.
func (c *Client) Search(ctx context.Context, q Query) (*Page, error) {
	cat, err := cleanCategory(q.Category)
	if err != nil {
		return nil, err
	}
	return c.listing(ctx, cat, "?"+q.params().Encode())
}

// Next fetches the page after p. It returns nil when p was the last.
func (c *Client) Next(ctx context.Context, p *Page) (*Page, error) {
	if !p.More() {
		return nil, nil
	}
	return c.listing(ctx, p.Category, p.next)
}

func (c *Client) listing(ctx context.Context, category, query string) (*Page, error) {
	seg := category
	if seg == "" {
		seg = "search"
	}
	var raw rawListing
	if err := c.get(ctx, listingAPI+seg+"/"+query, &raw); err != nil {
		if errors.Is(err, ErrNotFound) && category != "" {
			return nil, fmt.Errorf("%q: %w", category, ErrNoCategory)
		}
		return nil, err
	}
	// An unknown category is not a 404: the site quietly searches
	// everything instead, with no rubric in the answer.
	if category != "" && raw.Rubric.ID == 0 {
		return nil, fmt.Errorf("%q: %w", category, ErrNoCategory)
	}
	return raw.page(category), nil
}

func (q Query) params() url.Values {
	v := url.Values{}
	if q.Text != "" {
		v.Set("q", q.Text)
	}
	if q.Ordering != "" {
		v.Set("ordering", q.Ordering)
	}
	if q.PriceMin > 0 {
		v.Set("price_min", strconv.FormatInt(q.PriceMin, 10))
	}
	if q.PriceMax > 0 {
		v.Set("price_max", strconv.FormatInt(q.PriceMax, 10))
	}
	for k, vals := range q.Filters {
		for _, s := range vals {
			v.Add(k, s)
		}
	}
	size := q.PageSize
	if size <= 0 || size > MaxPageSize {
		size = MaxPageSize
	}
	v.Set("page_size", strconv.Itoa(size))
	return v
}

func (r *rawListing) page(category string) *Page {
	p := &Page{
		Count:         r.Count,
		Title:         r.Title,
		Subcategories: r.ChildRubrics,
		Category:      category,
	}
	if r.NextPage != nil && len(r.Adverts) > 0 {
		p.next = *r.NextPage
	}
	for i := range p.Subcategories {
		// The API gives "a/b/c/?q=…"; the query belongs to this search, not
		// to the category.
		path, _, _ := strings.Cut(p.Subcategories[i].Path, "?")
		p.Subcategories[i].Path = strings.Trim(path, "/")
	}
	for _, a := range r.Adverts {
		p.Items = append(p.Items, a.item())
	}
	return p
}

func (a rawAdvert) item() Item {
	it := Item{
		ID:        a.ID,
		Title:     strings.TrimSpace(a.Title),
		Currency:  a.Currency,
		PriceText: a.Price,
		PriceNote: a.PriceDescription,
		Location:  a.Location,
		Published: a.Published,
		URL:       absURL(a.URL),
		Slug:      slugFromURL(a.URL),
		Images:    a.Images,
		Seller: Seller{
			ID:       a.User.ID,
			Name:     a.User.Name,
			Business: a.User.IsBusiness,
			Verified: a.User.Verified || a.User.EMongoliaVerified,
		},
	}
	it.Price, _ = strconv.ParseInt(a.PriceRaw, 10, 64)
	if a.AdType != nil && a.AdType.Type != "regular" {
		it.Promoted = a.AdType.Text
	}
	for _, an := range a.Rubric.Ancestors {
		it.Category = append(it.Category, an.Name)
	}
	for _, l := range a.Labels {
		it.Labels = append(it.Labels, l.Text)
	}
	return it
}

// cleanCategory accepts a category path, with or without slashes, or a
// unegui.mn URL of a category page, and returns the bare path.
func cleanCategory(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return "", fmt.Errorf("category %q: %w", s, err)
		}
		if !isUneguiHost(u.Host) {
			return "", fmt.Errorf("category %q is not a unegui.mn address", s)
		}
		s = u.Path
	}
	s, _, _ = strings.Cut(s, "?")
	s = strings.Trim(s, "/")
	if strings.HasPrefix(s, "adv/") {
		return "", fmt.Errorf("%q is an advert, not a category: use `uneguictl show`", s)
	}
	if s == "search" {
		return "", nil
	}
	return s, nil
}

func isUneguiHost(h string) bool {
	return h == "unegui.mn" || strings.HasSuffix(h, ".unegui.mn")
}

func absURL(p string) string {
	if p == "" || strings.Contains(p, "://") {
		return p
	}
	return BaseURL + p
}
