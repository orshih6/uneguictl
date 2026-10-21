package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/orshih6/uneguictl/internal/claudeweb"
	"github.com/orshih6/uneguictl/internal/watch"
)

// fakeClaudeweb answers like claudeweb's API: a chat, then SSE replies.
type fakeClaudeweb struct {
	chats   int
	prompts []string
	parents []any
}

func (f *fakeClaudeweb) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer cw-token" {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	switch {
	case r.URL.Path == "/v1/chats":
		f.chats++
		if body["model"] != "sonnet" || fmt.Sprint(body["mcp_servers"]) != "[unegui]" {
			http.Error(w, `{"error":"wrong chat settings"}`, http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"id":"chat%d"}`, f.chats)
	case strings.HasSuffix(r.URL.Path, "/messages"):
		f.prompts = append(f.prompts, body["text"].(string))
		f.parents = append(f.parents, body["parent_id"])
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: text\ndata: {\"delta\":\"Hi\"}\n\n: keepalive\n\n")
		n := len(f.prompts)
		fmt.Fprintf(w, "event: done\ndata: {\"message\":{\"id\":\"a%d\",\"status\":\"done\",\"parts\":[{\"type\":\"tool\",\"name\":\"mcp__unegui__set_rule\"},{\"type\":\"text\",\"text\":\"Saved **promax**, see [it](https://www.unegui.mn/adv/1_x/) & more.\"}]}}\n\n", n)
	default:
		http.NotFound(w, r)
	}
}

func TestTalkContinuesTheClaudewebChat(t *testing.T) {
	fake := &fakeClaudeweb{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	w, rec := newTestWatcher(t, &fakeSite{})
	b := &bot{watcher: w, chat: "42", every: time.Hour,
		claude: &claudeweb.Client{URL: srv.URL, Token: "cw-token"}, model: "sonnet", connector: "unegui",
		chatURL: "https://claude.example"}
	ctx := context.Background()

	b.handle(ctx, "find me a 15 pro max")
	b.handle(ctx, "and watch for new ones")
	if fake.chats != 1 || len(fake.prompts) != 2 {
		t.Fatalf("%d chats, %d prompts", fake.chats, len(fake.prompts))
	}
	if !strings.HasPrefix(fake.prompts[0], "[I am writing from Telegram") || fake.prompts[1] != "and watch for new ones" {
		t.Errorf("prompts = %q", fake.prompts)
	}
	if fake.parents[0] != nil || fake.parents[1] != "a1" {
		t.Errorf("parents = %v", fake.parents)
	}
	got := rec.texts()
	if !strings.Contains(got, "Saved <b>promax</b>, see <a href=\"https://www.unegui.mn/adv/1_x/\">it</a> &amp; more.") {
		t.Errorf("reply not converted: %q", got)
	}
	if !strings.Contains(got, "https://claude.example/chat/chat1") {
		t.Errorf("first reply should link the claudeweb chat: %q", got)
	}

	b.handle(ctx, "/new")
	b.handle(ctx, "start over")
	var ref watch.ChatRef
	w.withState(func(st *watch.State) { ref = st.Chat })
	if fake.chats != 2 || ref.ID != "chat2" || ref.Parent != "a3" {
		t.Errorf("/new: %d chats, ref %+v", fake.chats, ref)
	}
}

func TestSplitMessage(t *testing.T) {
	long := strings.Repeat("word ", 200)
	parts := splitMessage(long+"\n"+long+"\n"+long, 2500)
	if len(parts) != 2 {
		t.Errorf("%d parts", len(parts))
	}
	for _, p := range parts {
		if len([]rune(p)) > 2500 {
			t.Errorf("part of %d characters", len([]rune(p)))
		}
	}
	if got := splitMessage("# Title\n1 < 2", 100); got[0] != "<b>Title</b>\n1 &lt; 2" {
		t.Errorf("got %q", got[0])
	}
}
