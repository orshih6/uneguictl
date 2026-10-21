package cli

import (
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/orshih6/uneguictl/internal/output"
	"github.com/orshih6/uneguictl/internal/unegui"
)

// maxLimit caps --limit so a mistyped number cannot walk the whole site: ten
// requests at the API's largest page.
const maxLimit = 10 * unegui.MaxPageSize

type searchOptions struct {
	category   string
	min, max   string
	sort       string
	filters    []string
	limit      int
	noPromoted bool
	match      bool
}

func newSearchCommand() *cobra.Command {
	var o searchOptions
	cmd := &cobra.Command{
		Use:   "search [words...]",
		Short: "Find adverts by words, category, price and filters",
		Long: `Find adverts by words, category, price and filters.

With no words, lists the newest adverts in --category (or the whole site).

The site always ranks paid adverts (VIP, Онцгой) above the rest. With
--sort cheapest or expensive, uneguictl re-sorts everything it fetched by price
so the order is right across them; raise --limit to look further than the
first page. Adverts with no price go last.

The site's word search is loose: "iphone 15" also finds anything with "15"
in it. Narrow it with --category, or keep only titles that contain every
word with --match.`,
		Example: `  uneguictl search iphone 15 pro --match --sort cheapest
  uneguictl search -c utas--dugaar/gar-utas-ba-utasnuud/iphone/iphone-15-pro --max 2.5m
  uneguictl search -c https://www.unegui.mn/avto-mashin/ prius --min 20m --max 40m
  uneguictl search -c utas--dugaar/gar-utas-ba-utasnuud/iphone -f attrs__bagtaamzh=7
  uneguictl search macbook --json | jq '.items[] | {title, price, url}'`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSearch(cmd, strings.Join(args, " "), o)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&o.category, "category", "c", "", "category path or unegui.mn category URL (see `uneguictl categories`)")
	f.StringVar(&o.min, "min", "", "lowest price in ₮; accepts 500k, 1.5m, 1500000")
	f.StringVar(&o.max, "max", "", "highest price in ₮; accepts 500k, 1.5m, 1500000")
	f.StringVarP(&o.sort, "sort", "s", "", "relevance, newest, cheapest or expensive (default relevance with words, newest without)")
	f.StringArrayVarP(&o.filters, "filter", "f", nil, "category filter as slug=value, repeatable (see `uneguictl filters`)")
	f.IntVarP(&o.limit, "limit", "n", 30, fmt.Sprintf("most adverts to show (at most %d)", maxLimit))
	f.BoolVar(&o.noPromoted, "no-promoted", false, "leave out paid (VIP, Онцгой) adverts")
	f.BoolVar(&o.match, "match", false, "keep only adverts whose title contains every search word")
	return cmd
}

// searchResult is what --json prints.
type searchResult struct {
	Query string        `json:"query,omitempty"`
	Title string        `json:"title"`
	Total int           `json:"total"`
	Items []unegui.Item `json:"items"`
}

func runSearch(cmd *cobra.Command, text string, o searchOptions) error {
	if o.limit < 1 || o.limit > maxLimit {
		return fmt.Errorf("--limit must be between 1 and %d", maxLimit)
	}
	q := unegui.Query{Category: o.category, Text: text}
	var err error
	if q.PriceMin, err = parsePrice(o.min); err != nil {
		return fmt.Errorf("--min: %w", err)
	}
	if q.PriceMax, err = parsePrice(o.max); err != nil {
		return fmt.Errorf("--max: %w", err)
	}
	if q.PriceMin > 0 && q.PriceMax > 0 && q.PriceMin > q.PriceMax {
		return fmt.Errorf("--min %d is above --max %d", q.PriceMin, q.PriceMax)
	}
	if q.Ordering, err = ordering(o.sort, text); err != nil {
		return err
	}
	if q.Filters, err = parseFilters(o.filters); err != nil {
		return err
	}
	// Filtering on our side throws some adverts away, so fetch full pages
	// rather than exactly --limit.
	byPrice := q.Ordering == "cheapest" || q.Ordering == "expensive"
	q.PageSize = o.limit
	if o.match || o.noPromoted || byPrice {
		q.PageSize = unegui.MaxPageSize
	}

	c, err := client()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	page, err := c.Search(ctx, q)
	if err != nil {
		return err
	}
	res := searchResult{Query: text, Title: page.Title, Total: page.Count}
	keep := itemFilter(text, o)
	// The site sends VIP adverts, then Онцгой, then the rest, each tier in
	// price order when sorted by price. So the true cheapest --limit are
	// among every promoted advert plus the first --limit regular ones: keep
	// fetching until that many regular ones are in, then sort them together.
	seen := map[int64]bool{}
	regular, fetched := 0, 0
	for {
		for _, it := range page.Items {
			fetched++
			if !seen[it.ID] && keep(it) {
				seen[it.ID] = true
				res.Items = append(res.Items, it)
				if it.Promoted == "" {
					regular++
				}
			}
		}
		enough := len(res.Items) >= o.limit
		if byPrice {
			enough = regular >= o.limit
		}
		if enough || fetched >= maxLimit || !page.More() {
			break
		}
		if page, err = c.Next(ctx, page); err != nil {
			return err
		}
	}
	if byPrice {
		sortByPrice(res.Items, q.Ordering == "expensive")
		if regular < o.limit && page.More() && !flagJSON {
			fmt.Fprintf(cmd.ErrOrStderr(), "Sorted the first %d adverts only; narrow the search for an exact order.\n", fetched)
		}
	}
	if len(res.Items) > o.limit {
		res.Items = res.Items[:o.limit]
	}
	if res.Items == nil {
		res.Items = []unegui.Item{}
	}

	if !flagJSON {
		note := fmt.Sprintf("%d adverts match", res.Total)
		if text != "" {
			note += fmt.Sprintf(" %q", text)
		}
		if page.Category != "" {
			note += " in " + page.Category
		}
		if o.match || o.noPromoted {
			note += fmt.Sprintf("; %d kept after --match/--no-promoted", len(res.Items))
		} else {
			note += fmt.Sprintf("; showing %d", len(res.Items))
		}
		fmt.Fprintln(cmd.ErrOrStderr(), note+".")
	}
	return emit(cmd, res, func(w io.Writer) error {
		if len(res.Items) == 0 {
			return nil
		}
		rows := make([][]string, 0, len(res.Items))
		for _, it := range res.Items {
			title := it.Title
			if it.Promoted != "" {
				title = "[" + it.Promoted + "] " + title
			}
			rows = append(rows, []string{
				priceCell(it.PriceText, it.PriceNote),
				output.Truncate(title, 55),
				output.Truncate(it.Location, 32),
				it.Published,
				it.URL,
			})
		}
		return output.Table(w, []string{"PRICE", "TITLE", "LOCATION", "POSTED", "URL"}, rows)
	})
}

func priceCell(text, note string) string {
	switch {
	case text == "":
		if note != "" {
			return note
		}
		return "-"
	case note != "":
		return text + "*"
	}
	return text
}

func ordering(s, text string) (string, error) {
	if s == "" {
		if text == "" {
			return "newest", nil
		}
		return "relevance", nil
	}
	s = strings.ToLower(s)
	for _, o := range unegui.Orderings {
		if s == o {
			return s, nil
		}
	}
	return "", fmt.Errorf("--sort %q: use one of %s", s, strings.Join(unegui.Orderings, ", "))
}

// itemFilter returns what decides whether an advert is kept, from --match and
// --no-promoted.
func itemFilter(text string, o searchOptions) func(unegui.Item) bool {
	words := strings.Fields(strings.ToLower(text))
	return func(it unegui.Item) bool {
		if o.noPromoted && it.Promoted != "" {
			return false
		}
		if o.match {
			title := strings.ToLower(it.Title)
			for _, w := range words {
				if !containsWord(title, w) {
					return false
				}
			}
		}
		return true
	}
}

// containsWord reports whether w appears in s not as part of a longer word
// or number, so "15" does not match "150" and "pro" does not match "probook".
func containsWord(s, w string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], w)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(w)
		before, _ := utf8.DecodeLastRuneInString(s[:start])
		after, _ := utf8.DecodeRuneInString(s[end:])
		if !isWordRune(before) && !isWordRune(after) {
			return true
		}
		_, size := utf8.DecodeRuneInString(s[start:])
		i = start + size
	}
}

// isWordRune is false for utf8.RuneError, which is what the decoders return
// at either end of the string.
func isWordRune(r rune) bool {
	return r != utf8.RuneError && (unicode.IsLetter(r) || unicode.IsDigit(r))
}

// sortByPrice orders items by price, keeping the site's order among equal
// prices and putting adverts without a price last.
func sortByPrice(items []unegui.Item, desc bool) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i].Price, items[j].Price
		if a == 0 || b == 0 {
			return a != 0 && b == 0
		}
		if desc {
			return a > b
		}
		return a < b
	})
}

// parsePrice reads a price in tögrög: a plain number, or one ending in k
// (thousand) or m / сая (million). Spaces and commas are ignored.
func parsePrice(s string) (int64, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0, nil
	}
	s = strings.NewReplacer(",", "", " ", "", "₮", "", "_", "").Replace(s)
	mult := 1.0
	for suffix, m := range map[string]float64{"k": 1e3, "m": 1e6, "сая": 1e6, "мянга": 1e3} {
		if strings.HasSuffix(s, suffix) {
			s, mult = strings.TrimSuffix(s, suffix), m
			break
		}
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%q is not a price (try 500k, 1.5m or 1500000)", s)
	}
	return int64(n*mult + 0.5), nil
}

func parseFilters(fs []string) (url.Values, error) {
	v := url.Values{}
	for _, f := range fs {
		k, val, ok := strings.Cut(f, "=")
		k, val = strings.TrimSpace(k), strings.TrimSpace(val)
		if !ok || k == "" || val == "" {
			return nil, fmt.Errorf("--filter %q: expected slug=value, e.g. attrs__bagtaamzh=7", f)
		}
		for _, part := range strings.Split(val, ",") {
			if part = strings.TrimSpace(part); part != "" {
				v.Add(k, part)
			}
		}
	}
	return v, nil
}
