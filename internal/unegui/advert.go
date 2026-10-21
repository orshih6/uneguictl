package unegui

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Advert is one advert's full page.
type Advert struct {
	ID          int64     `json:"id"`
	Title       string    `json:"title"`
	Price       int64     `json:"price"`
	PriceText   string    `json:"price_text"`
	PriceNote   string    `json:"price_note,omitempty"`
	Description string    `json:"description"`
	Location    string    `json:"location"`
	Published   string    `json:"published"`
	Views       int       `json:"views"`
	Favorites   int       `json:"favorites"`
	Closed      bool      `json:"closed"`
	URL         string    `json:"url"`
	Category    []string  `json:"category"`
	Features    []Feature `json:"features,omitempty"`
	Images      []string  `json:"images,omitempty"`
	Seller      Seller    `json:"seller"`
	// SellerSince is e.g. "Элссэн огноо Гуравдугаар сар 2024".
	SellerSince   string `json:"seller_since,omitempty"`
	SellerAdverts string `json:"seller_adverts,omitempty"`
	// Phone is filled only by Client.Phone; listings never carry it.
	Phone string `json:"phone,omitempty"`
}

// Feature is one attribute of an advert, e.g. Багтаамж = 256 gb.
type Feature struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type rawCard struct {
	ID       int64  `json:"id"`
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	IsClosed bool   `json:"is_closed"`
	Price    struct {
		Price       string `json:"price"`
		Description string `json:"description"`
		Raw         string `json:"price_without_currency"`
	} `json:"price"`
	Description string `json:"description"`
	Location    struct {
		Name string `json:"name"`
	} `json:"location"`
	Counters struct {
		Favorites  int    `json:"favorites"`
		AdvertView int    `json:"advert_view"`
		Published  string `json:"published"`
	} `json:"counters"`
	Gallery struct {
		Full []string `json:"full"`
	} `json:"gallery"`
	Features   []Feature `json:"features"`
	Categories []struct {
		Name string `json:"name"`
	} `json:"categories"`
	User struct {
		ID                int64  `json:"id"`
		Name              string `json:"name"`
		Posting           string `json:"posting"`
		ActiveAdverts     string `json:"active_adverts"`
		IsVerified        bool   `json:"is_verified"`
		IsEMongolia       bool   `json:"is_emongolia"`
		IsBusinessAccount bool   `json:"is_business_account"`
		IsCompany         bool   `json:"is_company"`
	} `json:"user"`
}

// slugRE matches the "<id>_<name>" part of an advert URL. The API looks
// adverts up by the whole of it: the id alone is a 404.
var slugRE = regexp.MustCompile(`^(\d+)_[^/?#]+$`)

// AdvertSlug turns what a person might paste — an advert URL, "/adv/…/" or
// the bare "<id>_<name>" — into the slug the API wants.
func AdvertSlug(s string) (string, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return "", fmt.Errorf("advert %q: %w", s, err)
		}
		if !isUneguiHost(u.Host) {
			return "", fmt.Errorf("%q is not a unegui.mn address", s)
		}
		s = u.Path
	}
	slug := slugFromURL(s)
	if slugRE.MatchString(slug) {
		return slug, nil
	}
	if _, err := strconv.ParseInt(slug, 10, 64); err == nil {
		return "", fmt.Errorf("advert %s: unegui.mn needs the whole address, not only the number; copy the URL or the slug from `uneguictl search` (e.g. %s_apple-iphone-15)", slug, slug)
	}
	return "", fmt.Errorf("%q is not an advert address (expected https://www.unegui.mn/adv/<id>_<name>/)", s)
}

func slugFromURL(p string) string {
	p, _, _ = strings.Cut(p, "?")
	p = strings.Trim(p, "/")
	p = strings.TrimPrefix(p, "adv/")
	return p
}

// Advert fetches one advert by its slug (see AdvertSlug).
func (c *Client) Advert(ctx context.Context, slug string) (*Advert, error) {
	var raw rawCard
	if err := c.get(ctx, "/api/v2/spa/adverts/card/"+slug+"/", &raw); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("advert %s: %w (removed, or the address is wrong)", slug, err)
		}
		return nil, err
	}
	if raw.ID == 0 {
		// A closed advert answers 404 with {"status":"gone"}, which get
		// already turned into ErrNotFound; anything else without an id is
		// a format change.
		return nil, fmt.Errorf("advert %s: the answer has no advert in it", slug)
	}
	return raw.advert(), nil
}

func (r *rawCard) advert() *Advert {
	a := &Advert{
		ID:            r.ID,
		Title:         strings.TrimSpace(r.Name),
		PriceText:     r.Price.Price,
		PriceNote:     r.Price.Description,
		Description:   strings.ReplaceAll(strings.TrimSpace(r.Description), "\r\n", "\n"),
		Location:      r.Location.Name,
		Published:     r.Counters.Published,
		Views:         r.Counters.AdvertView,
		Favorites:     r.Counters.Favorites,
		Closed:        r.IsClosed,
		URL:           fmt.Sprintf("%s/adv/%d_%s/", BaseURL, r.ID, r.Slug),
		Features:      r.Features,
		Images:        r.Gallery.Full,
		SellerSince:   r.User.Posting,
		SellerAdverts: r.User.ActiveAdverts,
		Seller: Seller{
			ID:       r.User.ID,
			Name:     r.User.Name,
			Business: r.User.IsBusinessAccount || r.User.IsCompany,
			Verified: r.User.IsVerified || r.User.IsEMongolia,
		},
	}
	a.Price, _ = strconv.ParseInt(r.Price.Raw, 10, 64)
	for _, c := range r.Categories {
		a.Category = append(a.Category, c.Name)
	}
	return a
}

// Phone fetches the seller's phone number for an advert. The site shows it
// without a sign-in but counts every request as a "phone view" the seller
// sees, so it is fetched only when asked for.
func (c *Client) Phone(ctx context.Context, id int64) (string, error) {
	var raw struct {
		Phone       string `json:"phone"`
		Permissions string `json:"permissions"`
	}
	if err := c.get(ctx, fmt.Sprintf("/api/items/get_phone/%d/", id), &raw); err != nil {
		return "", fmt.Errorf("phone for advert %d: %w", id, err)
	}
	if raw.Phone == "" {
		return "", fmt.Errorf("phone for advert %d: the site did not show one (%s)", id, raw.Permissions)
	}
	return raw.Phone, nil
}

// Category is one node of the site's category tree.
type Category struct {
	ID       int        `json:"id"`
	Name     string     `json:"name"`
	Path     string     `json:"path"`
	Count    int        `json:"count"`
	Children []Category `json:"children,omitempty"`
}

type rawRubric struct {
	ID       int         `json:"id"`
	Name     string      `json:"name"`
	Path     string      `json:"path"`
	AdvCount int         `json:"adv_count"`
	Children []rawRubric `json:"children"`
}

func (r rawRubric) category() Category {
	c := Category{ID: r.ID, Name: r.Name, Path: strings.Trim(r.Path, "/"), Count: r.AdvCount}
	for _, ch := range r.Children {
		c.Children = append(c.Children, ch.category())
	}
	return c
}

// Categories fetches the site's category tree. It goes two levels below the
// top; deeper levels come from a listing's Subcategories.
func (c *Client) Categories(ctx context.Context) ([]Category, error) {
	var raw []rawRubric
	if err := c.get(ctx, "/api/v2/spa/rubrics/rubricator/?limit=100&sort=none", &raw); err != nil {
		return nil, err
	}
	out := make([]Category, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.category())
	}
	return out, nil
}

// Filter is one of a category's search filters.
type Filter struct {
	Name string `json:"name"`
	// Slug is the query parameter; a range filter takes Slug_min and
	// Slug_max instead.
	Slug string `json:"slug"`
	// Range is true for numeric from–to filters such as price or year.
	Range   bool     `json:"range"`
	Unit    string   `json:"unit,omitempty"`
	Choices []Choice `json:"choices,omitempty"`
}

// Choice is one value a filter offers: Value goes in the query, Label is what
// the site shows.
type Choice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Filters fetches the filters a category offers.
func (c *Client) Filters(ctx context.Context, category string) ([]Filter, error) {
	cat, err := cleanCategory(category)
	if err != nil {
		return nil, err
	}
	if cat == "" {
		return nil, fmt.Errorf("filters belong to a category: name one (see `uneguictl categories`)")
	}
	// The filters endpoint answers for any path, with the site-wide price
	// filter for one it does not know; a one-advert listing tells them apart.
	if _, err := c.listing(ctx, cat, "?page_size=1"); err != nil {
		return nil, err
	}
	var raw struct {
		Filters []struct {
			Name        string   `json:"name"`
			Slug        string   `json:"slug"`
			ItemType    string   `json:"item_type"`
			MeasureUnit string   `json:"measure_unit"`
			Choices     [][2]any `json:"choices"`
		} `json:"filters"`
	}
	if err := c.get(ctx, "/api/v2/spa/adverts/filters/"+cat+"/", &raw); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("%q: %w", cat, ErrNoCategory)
		}
		return nil, err
	}
	out := make([]Filter, 0, len(raw.Filters))
	for _, f := range raw.Filters {
		flt := Filter{
			Name:  f.Name,
			Slug:  f.Slug,
			Range: strings.HasSuffix(f.ItemType, "_range"),
			Unit:  f.MeasureUnit,
		}
		for _, ch := range f.Choices {
			flt.Choices = append(flt.Choices, Choice{Value: scalar(ch[0]), Label: scalar(ch[1])})
		}
		out = append(out, flt)
	}
	return out, nil
}

// scalar prints a JSON string or number the way it appears in a URL.
func scalar(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}
