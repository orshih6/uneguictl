package cli

import (
	"context"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orshih6/uneguictl/internal/unegui"
	"github.com/orshih6/uneguictl/internal/watch"
)

// fakeSite serves adverts from memory, newest first.
type fakeSite struct {
	adverts []*unegui.Advert
	phones  int
}

func (f *fakeSite) Search(_ context.Context, q unegui.Query) (*unegui.Page, error) {
	p := &unegui.Page{}
	for _, a := range f.adverts {
		if q.PriceMax > 0 && a.Price > q.PriceMax {
			continue
		}
		p.Items = append(p.Items, unegui.Item{ID: a.ID, Title: a.Title, Price: a.Price, Slug: slugOf(a)})
	}
	return p, nil
}

func (f *fakeSite) Next(context.Context, *unegui.Page) (*unegui.Page, error) { return nil, nil }

func (f *fakeSite) Advert(_ context.Context, slug string) (*unegui.Advert, error) {
	for _, a := range f.adverts {
		if slugOf(a) == slug {
			c := *a
			return &c, nil
		}
	}
	return nil, unegui.ErrNotFound
}

func (f *fakeSite) Phone(context.Context, int64) (string, error) {
	f.phones++
	return "+97699999999", nil
}

func slugOf(a *unegui.Advert) string {
	return strings.Split(strings.TrimPrefix(a.URL, "https://www.unegui.mn/adv/"), "/")[0]
}

func advert(id, price int64, desc string) *unegui.Advert {
	return &unegui.Advert{
		ID: id, Title: "Apple iPhone 15 Pro Max, 256 gb", Price: price, PriceText: formatMNT(price),
		Description: desc, URL: "https://www.unegui.mn/adv/" + itoa(id) + "_apple-iphone/",
		Images: []string{"https://cdn/" + itoa(id) + ".webp"},
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

type sent struct{ text, image string }

// recorder keeps what was sent; the connector's background checks send too.
type recorder struct {
	mu   sync.Mutex
	msgs []sent
}

func (r *recorder) Send(_ context.Context, text, image string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, sent{text, image})
	return nil
}

// texts returns what was sent so far, joined.
func (r *recorder) texts() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for _, m := range r.msgs {
		b.WriteString(m.text + "\n---\n")
	}
	return b.String()
}

func newTestWatcher(t *testing.T, site *fakeSite) (*watcher, *recorder) {
	t.Helper()
	st, err := watch.Load(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	return &watcher{src: site, out: rec, state: st, log: io.Discard, now: time.Now}, rec
}

func TestWatchAnnouncesOnlyNewMatches(t *testing.T) {
	site := &fakeSite{adverts: []*unegui.Advert{
		advert(1, 2300000, "bh98"),
		advert(2, 2000000, "bh100 cc 50"),
		advert(3, 1500000, "bh86"),
		advert(4, 1900000, "battery solison bh100"),
	}}
	w, rec := newTestWatcher(t, site)
	r, err := parseRule(strings.Fields("promax category=x battery=90 cycles=100 exclude=solis"))
	if err != nil {
		t.Fatal(err)
	}
	w.state.SetRule(r)
	ctx := context.Background()

	if err := w.checkAll(ctx); err != nil {
		t.Fatal(err)
	}
	// Summary, then the two matches cheapest first.
	if len(rec.msgs) != 3 || !strings.Contains(rec.msgs[0].text, "2 adverts match") {
		t.Fatalf("first run sent %d: %+v", len(rec.msgs), rec.msgs)
	}
	if !strings.Contains(rec.msgs[1].text, "2,000,000₮") || !strings.Contains(rec.msgs[2].text, "2,300,000₮") {
		t.Errorf("not cheapest first: %q / %q", rec.msgs[1].text, rec.msgs[2].text)
	}
	if !strings.Contains(rec.msgs[1].text, "BH 100% · 50 cycles") || !strings.Contains(rec.msgs[1].text, "/phone_2") || rec.msgs[1].image != "https://cdn/2.webp" {
		t.Errorf("message lacks battery, phone link or photo: %+v", rec.msgs[1])
	}
	if site.phones != 0 {
		t.Error("the watch must never fetch a phone number by itself")
	}

	rec.msgs = nil
	if err := w.checkAll(ctx); err != nil {
		t.Fatal(err)
	}
	if len(rec.msgs) != 0 {
		t.Fatalf("second run repeated itself: %+v", rec.msgs)
	}

	site.adverts = append([]*unegui.Advert{advert(5, 2100000, "Battery Health: 95%")}, site.adverts...)
	site.adverts[2].Price = 1800000 // advert 2 got cheaper
	if err := w.checkAll(ctx); err != nil {
		t.Fatal(err)
	}
	if len(rec.msgs) != 2 {
		t.Fatalf("third run sent %d: %+v", len(rec.msgs), rec.msgs)
	}
	var drop, fresh bool
	for _, m := range rec.msgs {
		drop = drop || strings.Contains(m.text, "price dropped from 2,000,000₮")
		fresh = fresh || strings.Contains(m.text, "🆕") && strings.Contains(m.text, "BH 95%")
	}
	if !drop || !fresh {
		t.Errorf("want a price drop and a new match: %+v", rec.msgs)
	}
}

func TestWatchFirstRunListsTheRest(t *testing.T) {
	site := &fakeSite{}
	for i := int64(1); i <= 13; i++ {
		site.adverts = append(site.adverts, advert(i, 1000000+i*10000, "bh100"))
	}
	w, rec := newTestWatcher(t, site)
	w.state.SetRule(watch.Rule{Name: "all", Category: "x"})
	if err := w.checkAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rec.msgs) != 1+firstRunShown+1 || !strings.Contains(rec.msgs[len(rec.msgs)-1].text, "3 more") {
		t.Errorf("sent %d, last %q", len(rec.msgs), rec.msgs[len(rec.msgs)-1].text)
	}
}

func TestParseRule(t *testing.T) {
	r, err := parseRule(strings.Fields("promax iphone category=utas—dugaar/iphone q=15+pro max=2.5m battery=90% cycles=100 unstated=yes filter=attrs__bagtaamzh=7,12"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "promax" || r.Query != "iphone 15 pro" || r.Category != "utas--dugaar/iphone" || r.MinBattery != 90 ||
		r.MaxCycles != 100 || !r.KeepUnstated || r.Max != "2.5m" || len(r.Filters) != 1 {
		t.Errorf("rule = %+v", r)
	}
	for _, bad := range []string{
		"", "category=x", "promax", "promax category=x max=cheap", "promax category=x battery=150",
		"promax category=x exclude=(", "promax category=x color=red", "bad/name category=x",
	} {
		if _, err := parseRule(strings.Fields(bad)); err == nil {
			t.Errorf("parseRule(%q) should fail", bad)
		}
	}
}

func TestBotCommands(t *testing.T) {
	site := &fakeSite{adverts: []*unegui.Advert{advert(7, 2000000, "bh100")}}
	w, rec := newTestWatcher(t, site)
	b := &bot{watcher: w, chat: "42", every: time.Hour}
	ctx := context.Background()

	b.handle(ctx, "/add promax category=x battery=90")
	if w.state.Rule("promax") == nil || !w.state.Started("promax") {
		t.Fatal("/add should save the rule and check it")
	}
	b.handle(ctx, "/pause promax")
	if !w.state.Rule("promax").Paused {
		t.Error("/pause did not pause")
	}
	rec.msgs = nil
	b.handle(ctx, "/rules@uneguibot")
	if len(rec.msgs) != 1 || !strings.Contains(rec.msgs[0].text, "⏸") {
		t.Errorf("/rules = %+v", rec.msgs)
	}
	b.handle(ctx, "/phone_7")
	if site.phones != 1 || !strings.Contains(rec.msgs[len(rec.msgs)-1].text, "+97699999999") {
		t.Errorf("/phone_7: %d phone requests, %+v", site.phones, rec.msgs)
	}
	b.handle(ctx, "/remove promax")
	if len(w.state.Rules) != 0 {
		t.Error("/remove did not remove")
	}
}

func TestAdvertMessageFitsTelegram(t *testing.T) {
	a := advert(1, 2000000, strings.Repeat("<long & winding> ", 600))
	msg := advertMessage("🆕 x", a, watch.Battery{})
	if n := len([]rune(msg)); n > 4096 {
		t.Errorf("message is %d characters", n)
	}
	if strings.Contains(msg, "<long") {
		t.Error("description was not escaped")
	}
}

func TestEveryMenuCommandIsHandled(t *testing.T) {
	w, rec := newTestWatcher(t, &fakeSite{})
	b := &bot{watcher: w, chat: "42", every: time.Hour}
	for _, c := range botCommands {
		rec.msgs = nil
		b.handle(context.Background(), "/"+c.Command)
		for _, m := range rec.msgs {
			if strings.Contains(m.text, "I don't know that one") {
				t.Errorf("menu command /%s is not handled", c.Command)
			}
		}
	}
}
