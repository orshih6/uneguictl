package cli

import (
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/orshih6/uneguictl/internal/unegui"
	"github.com/orshih6/uneguictl/internal/watch"
)

// defaultRuleLimit is how many of the newest adverts a rule looks through
// when it does not say.
const defaultRuleLimit = 200

const ruleHelp = `<name> key=value…  (words without "=" are the search text)
  category=  category path, or a unegui.mn category URL copied from the site
  q=         search words, + for spaces (iphone+15+pro)
  min= max=  price: 500k, 1.5m, 2500000
  filter=    slug=value from ` + "`uneguictl filters`" + `, repeatable (filter=attrs__bagtaamzh=7)
  battery=   lowest battery health % the text must state
  cycles=    most charge cycles the text may state
  unstated=yes  also keep adverts that state neither
  include= exclude=  regular expression over title and description, repeatable
  match=yes  title must contain every search word
  nopromoted=yes  leave out VIP / Онцгой adverts
  limit=     newest adverts to look through (default 200, at most 1000)`

var ruleNameRE = regexp.MustCompile(`^[\p{L}\p{N}_-]{1,40}$`)

// parseRule reads a rule from the words after /add (or a --rule flag).
func parseRule(words []string) (watch.Rule, error) {
	var r watch.Rule
	if len(words) == 0 || strings.Contains(words[0], "=") {
		return r, fmt.Errorf("start with the rule's name, e.g. promax category=… battery=90")
	}
	if !ruleNameRE.MatchString(words[0]) {
		return r, fmt.Errorf("rule name %q: use letters, digits, - and _ only", words[0])
	}
	r.Name = words[0]
	var text []string
	for _, w := range words[1:] {
		k, v, ok := strings.Cut(w, "=")
		if !ok {
			text = append(text, w)
			continue
		}
		var err error
		switch strings.ToLower(k) {
		case "category", "cat", "c":
			// A phone keyboard turns the "--" in category paths into a dash.
			r.Category = strings.NewReplacer("—", "--", "–", "--").Replace(v)
		case "q", "query", "text":
			text = append(text, strings.Fields(strings.ReplaceAll(v, "+", " "))...)
		case "min":
			r.Min = v
		case "max":
			r.Max = v
		case "filter", "f":
			r.Filters = append(r.Filters, v)
		case "battery", "bh", "health":
			r.MinBattery, err = ruleInt(k, v, 1, 100)
		case "cycles", "cc", "cycle":
			r.MaxCycles, err = ruleInt(k, v, 1, 5000)
		case "limit", "n":
			r.Limit, err = ruleInt(k, v, 1, maxLimit)
		case "include":
			r.Include = append(r.Include, v)
		case "exclude":
			r.Exclude = append(r.Exclude, v)
		case "match":
			r.Match, err = ruleBool(k, v)
		case "nopromoted", "no-promoted", "no_promoted":
			r.NoPromoted, err = ruleBool(k, v)
		case "unstated":
			r.KeepUnstated, err = ruleBool(k, v)
		default:
			return r, fmt.Errorf("unknown key %q", k)
		}
		if err != nil {
			return r, err
		}
	}
	r.Query = strings.Join(text, " ")
	if r.Category == "" && r.Query == "" {
		return r, fmt.Errorf("give a category= or search words, or the rule would watch the whole site")
	}
	_, err := compileRule(r)
	return r, err
}

func ruleInt(k, v string, lo, hi int) (int, error) {
	n, err := strconv.Atoi(strings.TrimSuffix(v, "%"))
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("%s=%s: expected a number from %d to %d", k, v, lo, hi)
	}
	return n, nil
}

func ruleBool(k, v string) (bool, error) {
	switch strings.ToLower(v) {
	case "", "yes", "y", "true", "1", "on":
		return true, nil
	case "no", "n", "false", "0", "off":
		return false, nil
	}
	return false, fmt.Errorf("%s=%s: expected yes or no", k, v)
}

// compiledRule is a rule turned into a query and checks.
type compiledRule struct {
	watch.Rule
	query            unegui.Query
	keep             func(unegui.Item) bool
	include, exclude []*regexp.Regexp
	limit            int
}

func compileRule(r watch.Rule) (*compiledRule, error) {
	c := &compiledRule{Rule: r, limit: r.Limit}
	if c.limit == 0 {
		c.limit = defaultRuleLimit
	}
	q := unegui.Query{Category: r.Category, Text: r.Query, Ordering: "newest", PageSize: unegui.MaxPageSize}
	var err error
	if q.PriceMin, err = parsePrice(r.Min); err != nil {
		return nil, fmt.Errorf("min: %w", err)
	}
	if q.PriceMax, err = parsePrice(r.Max); err != nil {
		return nil, fmt.Errorf("max: %w", err)
	}
	if q.PriceMin > 0 && q.PriceMax > 0 && q.PriceMin > q.PriceMax {
		return nil, fmt.Errorf("min %d is above max %d", q.PriceMin, q.PriceMax)
	}
	if q.Filters, err = parseFilters(r.Filters); err != nil {
		return nil, err
	}
	c.query = q
	c.keep = itemFilter(r.Query, searchOptions{match: r.Match, noPromoted: r.NoPromoted})
	for _, s := range r.Include {
		re, err := regexp.Compile("(?i)" + s)
		if err != nil {
			return nil, fmt.Errorf("include=%s: %w", s, err)
		}
		c.include = append(c.include, re)
	}
	for _, s := range r.Exclude {
		re, err := regexp.Compile("(?i)" + s)
		if err != nil {
			return nil, fmt.Errorf("exclude=%s: %w", s, err)
		}
		c.exclude = append(c.exclude, re)
	}
	return c, nil
}

// matches decides on an advert's full page, which is where the description
// and so the battery figures are.
func (c *compiledRule) matches(a *unegui.Advert) (bool, watch.Battery) {
	text := a.Title + "\n" + a.Description
	bat := watch.ParseBattery(text)
	if a.Closed {
		return false, bat
	}
	if len(c.include) > 0 {
		hit := false
		for _, re := range c.include {
			hit = hit || re.MatchString(text)
		}
		if !hit {
			return false, bat
		}
	}
	for _, re := range c.exclude {
		if re.MatchString(text) {
			return false, bat
		}
	}
	return bat.Meets(c.MinBattery, c.MaxCycles, c.KeepUnstated), bat
}

// Telegram allows 4096 characters a message; leave room for the entities.
const messageBudget = 3800

// advertMessage is the whole advert as one Telegram message in its HTML
// subset, under header.
func advertMessage(header string, a *unegui.Advert, bat watch.Battery) string {
	e := html.EscapeString
	var b strings.Builder
	b.WriteString(header + "\n")
	fmt.Fprintf(&b, "<b><a href=\"%s\">%s</a></b>\n", e(a.URL), e(a.Title))
	price := a.PriceText
	if price == "" {
		price = "no price"
	}
	if a.PriceNote != "" {
		price += " · " + a.PriceNote
	}
	b.WriteString("💰 " + e(price) + "\n")
	if s := bat.String(); s != "" {
		b.WriteString("🔋 " + s + "\n")
	} else {
		b.WriteString("🔋 battery not stated\n")
	}
	for _, f := range a.Features {
		fmt.Fprintf(&b, "▫️ %s: %s\n", e(f.Name), e(f.Value))
	}
	if a.Location != "" {
		b.WriteString("📍 " + e(a.Location) + "\n")
	}
	fmt.Fprintf(&b, "🕒 %s · %d views\n", e(a.Published), a.Views)
	seller := a.Seller.Name
	var tags []string
	if a.Seller.Business {
		tags = append(tags, "business")
	}
	if a.Seller.Verified {
		tags = append(tags, "verified")
	}
	if a.SellerAdverts != "" {
		tags = append(tags, a.SellerAdverts)
	}
	if a.SellerSince != "" {
		tags = append(tags, a.SellerSince)
	}
	if len(tags) > 0 {
		seller += " (" + strings.Join(tags, ", ") + ")"
	}
	b.WriteString("👤 " + e(seller) + "\n")
	tail := fmt.Sprintf("\n📞 /phone_%d", a.ID)
	if a.Description != "" {
		room := messageBudget - utf8.RuneCountInString(b.String()) - utf8.RuneCountInString(tail) - 2
		b.WriteString("\n" + escapeTruncate(a.Description, room) + "\n")
	}
	b.WriteString(tail)
	return b.String()
}

// escapeTruncate HTML-escapes s and cuts it to at most n characters as
// Telegram counts them, never inside an entity such as &amp;.
func escapeTruncate(s string, n int) string {
	var b strings.Builder
	used := 0
	for _, r := range s {
		esc := html.EscapeString(string(r))
		if used+utf8.RuneCountInString(esc) > n-1 {
			return b.String() + "…"
		}
		b.WriteString(esc)
		used += utf8.RuneCountInString(esc)
	}
	return b.String()
}
