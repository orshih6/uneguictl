package watch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseBattery(t *testing.T) {
	// Taken from real iPhone adverts.
	cases := map[string]Battery{
		"Iphone 15promax 256gb bh100 cc 50 shinere zarna":            {100, 50},
		"256gb bh:86% negch zadargaa oroogui":                        {Health: 86},
		"Арын шил 15 pro max Blue BH-89% хэрэглээний сэвтэй":         {Health: 89},
		"256gb Blue zadargaa orogui Sim/orno Battery-91% Asuudalgui": {Health: 91},
		"Ip15 pro max 256gb battery health 88%tai":                   {Health: 88},
		"iphone 15pro max honkong battery healthy 80% ymarch":        {Health: 80},
		"Хэрэглээний сэв зураастай. Баттерей 89":                     {Health: 89},
		"🔋 Battery Health: 83% 🎨 Өнгө":                               {Health: 83},
		"сэв зураасгүй 15 pro max zarna bh 84 Cc986":                 {84, 986},
		"Bh 85% Ямар ч асуудалгүй":                                   {Health: 85},
		"Battery solisn oor asuudlgu 15plus zarna":                   {},
		"15promax 256gb, 1500cc engine":                              {},
		"iPhone 15 Pro Max 256gb":                                    {},
	}
	for text, want := range cases {
		if got := ParseBattery(text); got != want {
			t.Errorf("ParseBattery(%q) = %+v, want %+v", text, got, want)
		}
	}
}

func TestBatteryMeets(t *testing.T) {
	cases := []struct {
		b                Battery
		minH, maxC       int
		unstated, expect bool
	}{
		{Battery{}, 0, 0, false, true},
		{Battery{100, 50}, 90, 100, false, true},
		{Battery{Health: 100}, 90, 100, false, true},
		{Battery{Cycles: 40}, 90, 100, false, true},
		{Battery{Health: 86}, 90, 100, false, false},
		{Battery{100, 300}, 90, 100, false, false},
		{Battery{}, 90, 100, false, false},
		{Battery{}, 90, 100, true, true},
		{Battery{Health: 86}, 90, 0, true, false},
	}
	for _, c := range cases {
		if got := c.b.Meets(c.minH, c.maxC, c.unstated); got != c.expect {
			t.Errorf("%+v.Meets(%d, %d, %v) = %v", c.b, c.minH, c.maxC, c.unstated, got)
		}
	}
}

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s.SetRule(Rule{Name: "promax", Category: "a/b", MinBattery: 90})
	if s.Started("promax") {
		t.Error("a new rule has not started")
	}
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	s.Put("promax", 1, &Seen{Price: 2000000, Matched: true, FirstSeen: now, LastSeen: now})
	s.Put("promax", 2, &Seen{Price: 1, FirstSeen: now, LastSeen: now.Add(-40 * 24 * time.Hour)})
	s.Offset = 77
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	s, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Started("promax") || s.Offset != 77 || s.Rule("PROMAX") == nil {
		t.Fatalf("state did not survive: %+v", s)
	}
	s.Prune("promax", now.Add(-30*24*time.Hour))
	if _, ok := s.Get("promax", 2); ok {
		t.Error("an advert unseen for 40 days should be forgotten")
	}
	if seen, ok := s.Get("promax", 1); !ok || !seen.Matched {
		t.Error("a recent match should stay")
	}

	s.SetRule(Rule{Name: "promax", Category: "a/b", MinBattery: 95})
	if s.Started("promax") {
		t.Error("replacing a rule should forget what it saw")
	}
	if !s.RemoveRule("promax") || len(s.Rules) != 0 || s.Seen["promax"] != nil {
		t.Errorf("remove left %+v", s)
	}
}

func TestRuleString(t *testing.T) {
	r := Rule{Name: "promax", Category: "a/b", Query: "15 pro", Max: "2.5m", MinBattery: 90, Exclude: []string{"solis"}}
	want := "promax category=a/b q=15+pro max=2.5m battery=90 exclude=solis"
	if got := r.String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestTelegramSend(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/botTOKEN/sendMessage" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.FormValue("chat_id") != "42" || r.FormValue("parse_mode") != "HTML" || r.FormValue("text") != "hi" {
			t.Errorf("form %v", r.Form)
		}
		if !strings.Contains(r.FormValue("link_preview_options"), "img.webp") {
			t.Errorf("preview %q", r.FormValue("link_preview_options"))
		}
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"ok":false,"description":"Too Many Requests","parameters":{"retry_after":1}}`))
			return
		}
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()
	tg := &Telegram{Token: "TOKEN", ChatID: "42", API: srv.URL}
	if err := tg.Send(context.Background(), "hi", "https://cdn/img.webp"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("%d calls, want a retry after 429", calls)
	}
}

func TestTelegramErrorsHideToken(t *testing.T) {
	tg := &Telegram{Token: "SECRET", ChatID: "42", API: "http://127.0.0.1:1"}
	err := tg.Send(context.Background(), "hi", "")
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("err = %v", err)
	}
}

func TestTelegramUpdates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("offset") != "5" || r.FormValue("timeout") != "30" {
			t.Errorf("form %v", r.Form)
		}
		w.Write([]byte(`{"ok":true,"result":[{"update_id":5,"message":{"text":"/rules","chat":{"id":42}}},{"update_id":6}]}`))
	}))
	defer srv.Close()
	tg := &Telegram{Token: "T", ChatID: "42", API: srv.URL}
	msgs, err := tg.Updates(context.Background(), 5, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Text != "/rules" || msgs[0].ChatID != "42" || msgs[1].UpdateID != 6 {
		t.Errorf("msgs = %+v", msgs)
	}
}
