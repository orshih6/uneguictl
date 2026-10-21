// Package unegui reads public listings from unegui.mn.
//
// The site is a Next.js front end over a JSON API under /api/v2/spa/. The
// same endpoints the browser calls answer without a sign-in, but Cloudflare
// refuses any client whose TLS handshake does not look like a browser's, so
// requests go through tls-client with a Chrome profile instead of net/http.
package unegui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	http "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// BaseURL is the site every request goes to.
const BaseURL = "https://www.unegui.mn"

// The User-Agent has to name the same Chrome version as the TLS profile, or
// the two disagree and Cloudflare notices.
const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36"

var (
	// ErrBlocked means Cloudflare answered instead of the site.
	ErrBlocked = errors.New("unegui.mn refused the request (Cloudflare challenge); wait a while and try again")
	// ErrNotFound means the advert or category does not exist, or is gone.
	ErrNotFound = errors.New("not found on unegui.mn")
	// ErrNoCategory means a category path the site does not have.
	ErrNoCategory = errors.New("no such category on unegui.mn (see `uneguictl categories`)")
)

// Client makes requests to unegui.mn. It is safe for one goroutine at a time.
type Client struct {
	http tls_client.HttpClient
	base string
}

// New returns a client for BaseURL.
func New() (*Client, error) {
	c, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(),
		tls_client.WithClientProfile(profiles.Chrome_152),
		tls_client.WithTimeoutSeconds(30),
		tls_client.WithCookieJar(tls_client.NewCookieJar()),
	)
	if err != nil {
		return nil, fmt.Errorf("creating HTTP client: %w", err)
	}
	return &Client{http: c, base: BaseURL}, nil
}

// get fetches path (which starts with "/") and decodes the JSON answer into
// v. A 5xx or a dropped connection is tried once more after a pause; reads
// are safe to repeat.
func (c *Client) get(ctx context.Context, path string, v any) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
		var retry bool
		retry, err = c.getOnce(ctx, path, v)
		if !retry {
			return err
		}
	}
	return err
}

func (c *Client) getOnce(ctx context.Context, path string, v any) (retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return false, err
	}
	req.Header = http.Header{
		"accept":            {"application/json, text/plain, */*"},
		"accept-language":   {"mn,en;q=0.9"},
		"referer":           {c.base + "/"},
		"user-agent":        {userAgent},
		http.HeaderOrderKey: {"accept", "accept-language", "referer", "user-agent"},
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return true, fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return true, fmt.Errorf("GET %s: reading answer: %w", path, err)
	}
	isJSON := strings.Contains(resp.Header.Get("Content-Type"), "json")
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return false, ErrNotFound
	case resp.StatusCode == http.StatusForbidden && !isJSON,
		resp.StatusCode == http.StatusTooManyRequests:
		return false, ErrBlocked
	case resp.StatusCode >= 500:
		return true, fmt.Errorf("GET %s: unegui.mn answered %d", path, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return false, fmt.Errorf("GET %s: unegui.mn answered %d: %s", path, resp.StatusCode, snippet(body))
	case !isJSON:
		return false, fmt.Errorf("GET %s: expected JSON, got %q", path, resp.Header.Get("Content-Type"))
	}
	if err := json.Unmarshal(body, v); err != nil {
		return false, fmt.Errorf("GET %s: decoding answer: %w", path, err)
	}
	return false, nil
}

func snippet(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
