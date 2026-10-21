package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TelegramAPI is the Bot API's address.
const TelegramAPI = "https://api.telegram.org"

// Telegram talks to one chat through a bot. Plain net/http is fine here: only
// unegui.mn sits behind Cloudflare.
type Telegram struct {
	Token  string
	ChatID string
	// API defaults to TelegramAPI; tests point it elsewhere.
	API  string
	HTTP *http.Client
}

// Message is a text message someone sent the bot.
type Message struct {
	UpdateID int64
	ChatID   string
	Text     string
}

// Send posts text, written in Telegram's HTML subset, to the chat, with
// image (if not "") as a large preview above it. When Telegram says to slow
// down it waits as told and tries once more.
func (t *Telegram) Send(ctx context.Context, text, image string) error {
	form := url.Values{
		"chat_id":    {t.ChatID},
		"text":       {text},
		"parse_mode": {"HTML"},
	}
	preview := map[string]any{"is_disabled": true}
	if image != "" {
		preview = map[string]any{"url": image, "prefer_large_media": true, "show_above_text": true}
	}
	b, _ := json.Marshal(preview)
	form.Set("link_preview_options", string(b))
	for attempt := 0; ; attempt++ {
		wait, err := t.call(ctx, "sendMessage", form, nil)
		if wait == 0 || attempt > 0 {
			return err
		}
		if err := Sleep(ctx, wait); err != nil {
			return err
		}
	}
}

// Updates waits up to timeout for messages sent to the bot from offset on,
// in any chat; the caller decides whom to answer. The next offset is the
// last UpdateID + 1.
func (t *Telegram) Updates(ctx context.Context, offset int64, timeout time.Duration) ([]Message, error) {
	form := url.Values{
		"offset":          {strconv.FormatInt(offset, 10)},
		"timeout":         {strconv.Itoa(int(timeout / time.Second))},
		"allowed_updates": {`["message"]`},
	}
	var raw []struct {
		UpdateID int64 `json:"update_id"`
		Message  *struct {
			Text string `json:"text"`
			Chat struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		} `json:"message"`
	}
	if _, err := t.call(ctx, "getUpdates", form, &raw); err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(raw))
	for _, u := range raw {
		m := Message{UpdateID: u.UpdateID}
		if u.Message != nil {
			m.ChatID = strconv.FormatInt(u.Message.Chat.ID, 10)
			m.Text = u.Message.Text
		}
		out = append(out, m)
	}
	return out, nil
}

// call posts form to a Bot API method and decodes the result into out. It
// returns how long to wait when Telegram answers 429.
func (t *Telegram) call(ctx context.Context, method string, form url.Values, out any) (retryAfter time.Duration, err error) {
	api, client := t.API, t.HTTP
	if api == "" {
		api = TelegramAPI
	}
	if client == nil {
		// Longer than getUpdates' own wait.
		client = &http.Client{Timeout: 90 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, api+"/bot"+t.Token+"/"+method, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, fmt.Errorf("telegram %s: building the request failed", method)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		// The error names the URL, which holds the bot token.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return 0, fmt.Errorf("telegram %s: %w", method, err)
	}
	defer resp.Body.Close()
	var answer struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err := json.Unmarshal(body, &answer); err != nil {
		return 0, fmt.Errorf("telegram %s answered %d with something that is not JSON", method, resp.StatusCode)
	}
	if !answer.OK {
		err = fmt.Errorf("telegram %s: %s", method, answer.Description)
		if resp.StatusCode == http.StatusTooManyRequests && answer.Parameters.RetryAfter > 0 {
			return time.Duration(answer.Parameters.RetryAfter) * time.Second, err
		}
		return 0, err
	}
	if out != nil {
		if err := json.Unmarshal(answer.Result, out); err != nil {
			return 0, fmt.Errorf("telegram %s: decoding the answer: %w", method, err)
		}
	}
	return 0, nil
}

// Sleep waits for d, or until ctx is done.
func Sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
