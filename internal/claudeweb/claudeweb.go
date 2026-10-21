// Package claudeweb talks to the user's claudeweb API (orshih6/claudeweb), a
// Claude Code chat service on the same cluster, so the watch bot's Telegram
// chat is a claudeweb chat with the unegui connector turned on.
package claudeweb

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client calls one claudeweb API.
type Client struct {
	// URL is the API's base, e.g. http://claudeweb-api.claudeweb.svc.cluster.local:8080.
	URL   string
	Token string
	HTTP  *http.Client
}

// Chat is the part of a claudeweb chat the bot keeps.
type Chat struct {
	ID string `json:"id"`
}

// NewChat starts a chat with model and the named connectors turned on.
func (c *Client) NewChat(ctx context.Context, title, model string, connectors []string) (*Chat, error) {
	body := map[string]any{"title": title, "model": model, "web": true, "mcp_servers": connectors}
	var chat Chat
	if err := c.do(ctx, http.MethodPost, "/v1/chats", body, &chat); err != nil {
		return nil, err
	}
	if chat.ID == "" {
		return nil, errors.New("claudeweb: the new chat has no id")
	}
	return &chat, nil
}

// Reply is a finished assistant message.
type Reply struct {
	// ID is what the next message names as its parent.
	ID     string
	Text   string
	Status string
	// Tools are the tools Claude used, in order, e.g. mcp__unegui__search.
	Tools []string
}

type message struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Parts  []struct {
		Type string `json:"type"`
		Text string `json:"text"`
		Name string `json:"name"`
	} `json:"parts"`
}

func (m message) reply() *Reply {
	r := &Reply{ID: m.ID, Status: m.Status}
	var texts []string
	for _, p := range m.Parts {
		switch p.Type {
		case "text":
			texts = append(texts, p.Text)
		case "tool":
			r.Tools = append(r.Tools, p.Name)
		}
	}
	r.Text = strings.TrimSpace(strings.Join(texts, "\n\n"))
	return r
}

// ErrBusy means the chat is still answering an earlier message.
var ErrBusy = errors.New("claudeweb is still answering the previous message")

// Send posts text to chat after the assistant message parent ("" for the
// first) and waits for the whole reply, which comes as server-sent events.
func (c *Client) Send(ctx context.Context, chatID, parent, text string) (*Reply, error) {
	body := map[string]any{"parent_id": nil, "text": text}
	if parent != "" {
		body["parent_id"] = parent
	}
	resp, err := c.request(ctx, http.MethodPost, "/v1/chats/"+chatID+"/messages", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return nil, ErrBusy
	}
	if resp.StatusCode != http.StatusOK {
		return nil, apiError(resp)
	}
	var lastErr string
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	var event string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data := []byte(strings.TrimPrefix(line, "data: "))
			switch event {
			case "error":
				var e struct {
					Error string `json:"error"`
				}
				_ = json.Unmarshal(data, &e)
				lastErr = e.Error
			case "done":
				var d struct {
					Message message `json:"message"`
				}
				if err := json.Unmarshal(data, &d); err != nil {
					return nil, fmt.Errorf("claudeweb: reading the reply: %w", err)
				}
				r := d.Message.reply()
				if r.Text == "" && lastErr != "" {
					return r, fmt.Errorf("claudeweb: %s", lastErr)
				}
				return r, nil
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("claudeweb: the reply stopped: %w", err)
	}
	if lastErr != "" {
		return nil, fmt.Errorf("claudeweb: %s", lastErr)
	}
	return nil, errors.New("claudeweb: the reply ended without a message")
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	resp, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return apiError(resp)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		// A reply with tool use can take minutes; claudeweb itself stops a
		// turn after 30.
		client = &http.Client{Timeout: 31 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("claudeweb: %w", err)
	}
	return resp, nil
}

func apiError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		return fmt.Errorf("claudeweb answered %d: %s", resp.StatusCode, e.Error)
	}
	return fmt.Errorf("claudeweb answered %d", resp.StatusCode)
}
