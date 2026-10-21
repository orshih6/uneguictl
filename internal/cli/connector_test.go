package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/orshih6/uneguictl/internal/unegui"
	"github.com/orshih6/uneguictl/internal/watch"
)

const testToken = "0123456789abcdef0123456789abcdef"

type bearer string

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+string(b))
	return http.DefaultTransport.RoundTrip(r)
}

func connect(t *testing.T, b *bot) *mcp.ClientSession {
	t.Helper()
	h, err := newConnector(b, testToken)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/mcp", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: %d, want 401", resp.StatusCode)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: srv.URL + "/mcp", HTTPClient: &http.Client{Transport: bearer(testToken)}, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

func TestConnectorManagesRules(t *testing.T) {
	site := &fakeSite{adverts: []*unegui.Advert{advert(2, 2000000, "bh100 cc 50")}}
	w, rec := newTestWatcher(t, site)
	b := &bot{watcher: w, chat: "42", every: time.Hour}
	cs := connect(t, b)

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
		if strings.Contains(strings.ToLower(tl.Name), "phone") {
			t.Errorf("the connector must not offer %s", tl.Name)
		}
	}
	if got := strings.Join(names, " "); !strings.Contains(got, "set_rule") || !strings.Contains(got, "search") {
		t.Errorf("tools = %s", got)
	}

	if out, isErr := call(t, cs, "set_rule", map[string]any{"rule": "promax category=x battery=90 original=yes"}); isErr {
		t.Fatalf("set_rule: %s", out)
	}
	// The rule's first check runs in the background and reports to Telegram.
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(rec.texts(), "1 adverts match") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := rec.texts(); !strings.Contains(got, "🔧 Claude saved") || !strings.Contains(got, "1 adverts match") {
		t.Errorf("Telegram got %q", got)
	}

	out, _ := call(t, cs, "list_rules", nil)
	var listed struct {
		Rules []ruleInfo `json:"rules"`
	}
	if err := json.Unmarshal([]byte(out), &listed); err != nil || len(listed.Rules) != 1 || listed.Rules[0].Matched != 1 {
		t.Errorf("list_rules = %s (%v)", out, err)
	}

	if out, isErr := call(t, cs, "set_rule", map[string]any{"rule": "bad category=x battery=500"}); !isErr {
		t.Errorf("a bad rule should be an error, got %s", out)
	}
	if _, isErr := call(t, cs, "pause_rule", map[string]any{"name": "promax"}); isErr {
		t.Error("pause_rule failed")
	}
	var paused bool
	w.withState(func(st *watch.State) { paused = st.Rule("promax").Paused })
	if !paused {
		t.Error("pause_rule did not pause")
	}
	if _, isErr := call(t, cs, "remove_rule", map[string]any{"name": "nope"}); !isErr {
		t.Error("removing a missing rule should be an error")
	}
	if site.phones != 0 {
		t.Error("the connector fetched a phone number")
	}
}

func TestConnectorRejectsShortToken(t *testing.T) {
	w, _ := newTestWatcher(t, &fakeSite{})
	if _, err := newConnector(&bot{watcher: w}, "short"); err == nil {
		t.Error("a short token should be refused")
	}
}
