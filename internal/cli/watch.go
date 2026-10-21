package cli

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/orshih6/uneguictl/internal/claudeweb"
	"github.com/orshih6/uneguictl/internal/unegui"
	"github.com/orshih6/uneguictl/internal/watch"
)

const (
	// firstRunShown is how many matches a new rule announces in full; the
	// rest go in one list, so adding a rule does not flood the chat.
	firstRunShown = 10
	// forgetAfter drops adverts a rule has not seen for this long.
	forgetAfter = 30 * 24 * time.Hour
	// advertPause spaces out fetches of advert pages, to be gentle.
	advertPause = 700 * time.Millisecond
)

type watchOptions struct {
	state     string
	every     time.Duration
	once      bool
	rules     []string
	connector string
}

func newWatchCommand() *cobra.Command {
	var o watchOptions
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Run saved searches on a schedule and send new matches to Telegram",
		Long: `Run saved searches on a schedule and send new matches to Telegram.

watch is a Telegram bot. It runs every rule each --every, and sends each new
matching advert in full: price, battery, features, location, seller,
description, first photo and link. When a matching advert's price drops, it
says so. Rules are managed by messaging the bot:

  /add <rule>      add a rule, or replace the one with that name
  /rules           list the rules
  /remove <name>   /pause <name>   /resume <name>
  /check [name]    run the rules now
  /phone <id>      the seller's number for one advert (counted as a phone view)

A rule is ` + ruleHelp + `

Battery health and cycles are read from what the seller wrote ("bh 100",
"Battery 91%", "cc 50"), so a seller who does not say cannot be checked; an
advert must state at least one of the figures a rule bounds, unless
unstated=yes.

The bot token and chat come from TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID. It
obeys only that chat. Rules, what each rule has seen and the Telegram
position are kept in --state.

The seller's phone number is never fetched on its own: each advert carries a
/phone_<id> link to ask for it.

Any other message goes to Claude in claudeweb (CLAUDEWEB_API_URL,
CLAUDEWEB_API_TOKEN; CLAUDEWEB_MODEL, default sonnet), in a chat with the
connector CLAUDEWEB_CONNECTOR (default unegui) turned on; /new starts a new
chat. --connector serves that connector: unegui.mn lookups and the rules over
MCP, behind UNEGUICTL_CONNECTOR_TOKEN, with no phone tool.

With --once, watch runs the rules one time and exits, without reading
Telegram; with no TELEGRAM_* set it prints the messages instead of sending.`,
		Example: `  TELEGRAM_BOT_TOKEN=… TELEGRAM_CHAT_ID=… uneguictl watch --state /var/lib/uneguictl/state.json
  uneguictl watch --once --state /tmp/w.json --rule 'promax category=utas--dugaar/gar-utas-ba-utasnuud/iphone/iphone-15-pro-max battery=90 cycles=100 exclude=solis'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWatch(cmd, o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.state, "state", "uneguictl-watch.json", "file keeping the rules and what they have seen")
	f.DurationVar(&o.every, "every", 15*time.Minute, "how often to run the rules")
	f.BoolVar(&o.once, "once", false, "run the rules once and exit, without reading Telegram")
	f.StringArrayVar(&o.rules, "rule", nil, "add or replace a rule before starting, as /add takes it (repeatable)")
	f.StringVar(&o.connector, "connector", "", "serve the MCP connector for claudeweb on this address, e.g. :8790 (needs UNEGUICTL_CONNECTOR_TOKEN)")
	return cmd
}

func runWatch(cmd *cobra.Command, o watchOptions) error {
	if o.every < time.Minute {
		return fmt.Errorf("--every must be at least 1m")
	}
	st, err := watch.Load(o.state)
	if err != nil {
		return err
	}
	for _, s := range o.rules {
		r, err := parseRule(splitArgs(s))
		if err != nil {
			return fmt.Errorf("--rule %q: %w", s, err)
		}
		st.SetRule(r)
	}
	if err := st.Save(); err != nil {
		return err
	}
	c, err := client()
	if err != nil {
		return err
	}
	w := &watcher{src: c, state: st, log: cmd.ErrOrStderr(), pause: advertPause, now: time.Now}

	token, chat := os.Getenv("TELEGRAM_BOT_TOKEN"), os.Getenv("TELEGRAM_CHAT_ID")
	var tg *watch.Telegram
	if token != "" && chat != "" {
		tg = &watch.Telegram{Token: token, ChatID: chat}
		w.out = tg
	} else if !o.once {
		return fmt.Errorf("set TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID (or use --once to print instead)")
	} else {
		w.out = printer{cmd.OutOrStdout()}
	}

	ctx := cmd.Context()
	if o.once {
		return w.checkAll(ctx)
	}
	b := &bot{watcher: w, tg: tg, chat: chat, every: o.every}
	if u := os.Getenv("CLAUDEWEB_API_URL"); u != "" {
		b.claude = &claudeweb.Client{URL: u, Token: os.Getenv("CLAUDEWEB_API_TOKEN")}
		b.model = envOr("CLAUDEWEB_MODEL", "sonnet")
		b.connector = envOr("CLAUDEWEB_CONNECTOR", "unegui")
		b.chatURL = strings.TrimRight(os.Getenv("CLAUDEWEB_WEB_URL"), "/")
	}
	if o.connector != "" {
		h, err := newConnector(b, os.Getenv("UNEGUICTL_CONNECTOR_TOKEN"))
		if err != nil {
			return err
		}
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() {
			if err := serveConnector(ctx, o.connector, h); err != nil {
				fmt.Fprintf(w.log, "connector: %v\n", err)
				cancel()
			}
		}()
		fmt.Fprintf(w.log, "connector listening on %s/mcp\n", o.connector)
		return b.run(ctx)
	}
	return b.run(ctx)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// sender delivers a message; *watch.Telegram is the real one.
type sender interface {
	Send(ctx context.Context, text, image string) error
}

// printer shows messages on a terminal instead of sending them.
type printer struct{ w io.Writer }

var tagRE = regexp.MustCompile(`<[^>]+>`)

func (p printer) Send(_ context.Context, text, image string) error {
	text = html.UnescapeString(tagRE.ReplaceAllString(text, ""))
	if image != "" {
		text = "🖼 " + image + "\n" + text
	}
	_, err := fmt.Fprintf(p.w, "%s\n\n", text)
	return err
}

// watchSource is what the watch reads from unegui.mn.
type watchSource interface {
	Search(ctx context.Context, q unegui.Query) (*unegui.Page, error)
	Next(ctx context.Context, p *unegui.Page) (*unegui.Page, error)
	Advert(ctx context.Context, slug string) (*unegui.Advert, error)
	Phone(ctx context.Context, id int64) (string, error)
}

type watcher struct {
	// mu guards state and src: the Telegram loop, the scheduled checks and
	// the connector all reach them.
	mu    sync.Mutex
	src   watchSource
	out   sender
	state *watch.State
	log   io.Writer
	pause time.Duration
	now   func() time.Time
}

// withState runs fn on the state under the lock and saves it after.
func (w *watcher) withState(fn func(st *watch.State)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	fn(w.state)
	w.save()
}

// checkAll runs every rule that is not paused. It goes on past a failing
// rule and returns the first error.
func (w *watcher) checkAll(ctx context.Context) error {
	var names []string
	w.withState(func(st *watch.State) {
		for _, r := range st.Rules {
			if !r.Paused {
				names = append(names, r.Name)
			}
		}
	})
	var first error
	for _, name := range names {
		if err := w.checkRule(ctx, name); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Fprintf(w.log, "%v\n", err)
			if first == nil {
				first = err
			}
		}
	}
	return first
}

// checkRule runs the rule called name, if it still exists, holding the lock
// for the whole run.
func (w *watcher) checkRule(ctx context.Context, name string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	r := w.state.Rule(name)
	if r == nil {
		return nil
	}
	if err := w.check(ctx, *r); err != nil {
		return fmt.Errorf("rule %s: %w", r.Name, err)
	}
	return nil
}

type match struct {
	advert  *unegui.Advert
	battery watch.Battery
}

// check runs one rule: it lists the newest adverts, opens each one it has
// not seen before, and announces the ones that match. A matching advert it
// has seen before is announced again only when its price dropped.
func (w *watcher) check(ctx context.Context, r watch.Rule) error {
	c, err := compileRule(r)
	if err != nil {
		return err
	}
	items, err := w.list(ctx, c)
	if err != nil {
		return err
	}
	started := w.state.Started(r.Name)
	defer w.save()
	now := w.now()
	var fresh []match
	for _, it := range items {
		seen, ok := w.state.Get(r.Name, it.ID)
		if !ok {
			a, err := w.advert(ctx, it.Slug)
			if errors.Is(err, unegui.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if ok, bat := c.matches(a); ok {
				fresh = append(fresh, match{a, bat})
			} else {
				w.state.Put(r.Name, it.ID, &watch.Seen{Price: it.Price, FirstSeen: now, LastSeen: now})
			}
			continue
		}
		if seen.Matched && it.Price > 0 && seen.Price > 0 && it.Price < seen.Price {
			a, err := w.advert(ctx, it.Slug)
			if err == nil && !a.Closed {
				_, bat := c.matches(a)
				header := fmt.Sprintf("📉 <b>%s</b>: price dropped from %s", html.EscapeString(r.Name), formatMNT(seen.Price))
				if err := w.out.Send(ctx, advertMessage(header, a, bat), firstImage(a)); err != nil {
					return err
				}
			}
		}
		if it.Price > 0 {
			seen.Price = it.Price
		}
		seen.LastSeen = now
	}

	shown := fresh
	if !started {
		// A new rule finds everything already listed: show the cheapest.
		sort.SliceStable(fresh, func(i, j int) bool {
			a, b := fresh[i].advert.Price, fresh[j].advert.Price
			return a != 0 && (b == 0 || a < b)
		})
		if len(shown) > firstRunShown {
			shown = fresh[:firstRunShown]
		}
		summary := fmt.Sprintf("🔎 <b>%s</b>: %d adverts match now", html.EscapeString(r.Name), len(fresh))
		if len(fresh) > 0 {
			summary += ", cheapest first"
		}
		if err := w.out.Send(ctx, summary+".", ""); err != nil {
			return err
		}
	}
	for _, m := range shown {
		header := "🆕 <b>" + html.EscapeString(r.Name) + "</b>"
		if err := w.out.Send(ctx, advertMessage(header, m.advert, m.battery), firstImage(m.advert)); err != nil {
			return err
		}
		w.record(r.Name, m.advert, now)
	}
	if rest := fresh[len(shown):]; len(rest) > 0 {
		if err := w.out.Send(ctx, restMessage(r.Name, rest), ""); err != nil {
			return err
		}
		for _, m := range rest {
			w.record(r.Name, m.advert, now)
		}
	}
	w.state.Prune(r.Name, now.Add(-forgetAfter))
	return nil
}

func (w *watcher) record(rule string, a *unegui.Advert, now time.Time) {
	w.state.Put(rule, a.ID, &watch.Seen{Price: a.Price, Matched: true, FirstSeen: now, LastSeen: now})
}

func (w *watcher) save() {
	if err := w.state.Save(); err != nil {
		fmt.Fprintf(w.log, "saving state: %v\n", err)
	}
}

// list fetches the newest adverts for a rule, up to its limit.
func (w *watcher) list(ctx context.Context, c *compiledRule) ([]unegui.Item, error) {
	page, err := w.src.Search(ctx, c.query)
	if err != nil {
		return nil, err
	}
	var items []unegui.Item
	seen := map[int64]bool{}
	for fetched := 0; ; {
		for _, it := range page.Items {
			fetched++
			if !seen[it.ID] && c.keep(it) {
				seen[it.ID] = true
				items = append(items, it)
			}
		}
		if fetched >= c.limit || !page.More() {
			return items, nil
		}
		if page, err = w.src.Next(ctx, page); err != nil {
			return nil, err
		}
	}
}

func (w *watcher) advert(ctx context.Context, slug string) (*unegui.Advert, error) {
	if w.pause > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(w.pause):
		}
	}
	return w.src.Advert(ctx, slug)
}

func firstImage(a *unegui.Advert) string {
	if len(a.Images) == 0 {
		return ""
	}
	return a.Images[0]
}

// restMessage lists the matches a new rule did not show in full, one line
// each.
func restMessage(rule string, rest []match) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s</b>: %d more\n", html.EscapeString(rule), len(rest))
	for i, m := range rest {
		line := fmt.Sprintf("\n%s · <a href=\"%s\">%s</a>", html.EscapeString(m.advert.PriceText), html.EscapeString(m.advert.URL), html.EscapeString(m.advert.Title))
		if s := m.battery.String(); s != "" {
			line += " · " + s
		}
		if b.Len()+len(line) > messageBudget {
			fmt.Fprintf(&b, "\n… and %d more", len(rest)-i)
			break
		}
		b.WriteString(line)
	}
	return b.String()
}

// formatMNT is e.g. "2,000,000₮".
func formatMNT(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s + "₮"
}

// bot reads commands from Telegram between runs of the rules.
type bot struct {
	*watcher
	tg    *watch.Telegram
	chat  string
	every time.Duration
	// claude, if set, answers messages that are not commands, in a
	// claudeweb chat with the connector named connector turned on.
	claude    *claudeweb.Client
	model     string
	connector string
	// chatURL is claudeweb's web address, to link a conversation.
	chatURL string
	// ctx is run's context, for checks the connector starts.
	ctx context.Context

	errMu sync.Mutex
	// lastErr is the last check failure reported to the chat, so a
	// lasting one (Cloudflare, say) is reported once, not every run.
	lastErr string
}

func (b *bot) run(ctx context.Context) error {
	b.ctx = ctx
	var rules int
	b.withState(func(st *watch.State) { rules = len(st.Rules) })
	fmt.Fprintf(b.log, "watching %d rules every %s\n", rules, b.every)
	if err := b.tg.SetCommands(ctx, botCommands); err != nil {
		fmt.Fprintf(b.log, "setting the command menu: %v\n", err)
	}
	next := time.Now()
	for {
		if !time.Now().Before(next) {
			b.checkAndReport(ctx, "")
			next = time.Now().Add(b.every)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		wait := min(time.Until(next), 50*time.Second)
		wait = max(wait, time.Second)
		var offset int64
		b.withState(func(st *watch.State) { offset = st.Offset })
		msgs, err := b.tg.Updates(ctx, offset, wait)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Fprintf(b.log, "%v\n", err)
			if err := watch.Sleep(ctx, 10*time.Second); err != nil {
				return err
			}
			continue
		}
		for _, m := range msgs {
			b.withState(func(st *watch.State) { st.Offset = m.UpdateID + 1 })
			if m.ChatID != b.chat {
				fmt.Fprintf(b.log, "ignoring a message from chat %s\n", m.ChatID)
				continue
			}
			if b.handle(ctx, m.Text) {
				next = time.Now().Add(b.every)
			}
		}
	}
}

// checkAndReport runs one rule (or all when name is "") and tells the chat
// about a failure it has not reported yet.
func (b *bot) checkAndReport(ctx context.Context, name string) {
	var err error
	if name == "" {
		err = b.checkAll(ctx)
	} else {
		err = b.checkRule(ctx, name)
	}
	b.errMu.Lock()
	defer b.errMu.Unlock()
	if err == nil || ctx.Err() != nil {
		b.lastErr = ""
		return
	}
	fmt.Fprintf(b.log, "%v\n", err)
	if msg := err.Error(); msg != b.lastErr {
		b.lastErr = msg
		b.reply(ctx, "⚠️ "+html.EscapeString(msg))
	}
}

// checkSoon runs a rule in the background, for the connector, which should
// not wait for a check that can take a minute.
func (b *bot) checkSoon(name string) {
	ctx := b.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	go b.checkAndReport(ctx, name)
}

func (b *bot) reply(ctx context.Context, text string) {
	if err := b.out.Send(ctx, text, ""); err != nil {
		fmt.Fprintf(b.log, "%v\n", err)
	}
}

// botCommands is the menu Telegram shows when you type "/"; handle must
// understand each of them.
var botCommands = []watch.Command{
	{Command: "add", Description: "Add or replace a rule: /add <name> key=value…"},
	{Command: "rules", Description: "List the rules"},
	{Command: "check", Description: "Run the rules now: /check [name]"},
	{Command: "pause", Description: "Pause a rule: /pause <name>"},
	{Command: "resume", Description: "Resume a paused rule: /resume <name>"},
	{Command: "remove", Description: "Delete a rule: /remove <name>"},
	{Command: "phone", Description: "Seller's number for one advert: /phone <id>"},
	{Command: "new", Description: "Start a new conversation with Claude"},
	{Command: "help", Description: "How rules work, with an example"},
}

var phoneCmdRE = regexp.MustCompile(`^/phone_(\d+)$`)

// handle acts on one message: a command, or anything else for Claude. It
// reports whether it ran every rule, so the next scheduled run can wait a
// full interval.
func (b *bot) handle(ctx context.Context, text string) (ranAll bool) {
	words := strings.Fields(text)
	if len(words) == 0 {
		return false
	}
	if !strings.HasPrefix(words[0], "/") {
		b.talk(ctx, text)
		return false
	}
	cmd, _, _ := strings.Cut(strings.ToLower(words[0]), "@")
	args := words[1:]
	if m := phoneCmdRE.FindStringSubmatch(cmd); m != nil {
		cmd, args = "/phone", []string{m[1]}
	}
	e := html.EscapeString
	switch cmd {
	case "/add":
		_, rest, _ := strings.Cut(strings.TrimSpace(text), " ")
		r, err := parseRule(splitArgs(rest))
		if err != nil {
			b.reply(ctx, "❌ "+e(err.Error())+"\n\n<pre>/add "+e(ruleHelp)+"</pre>")
			return false
		}
		b.withState(func(st *watch.State) { st.SetRule(r) })
		b.reply(ctx, "✅ Saved <code>"+e(r.String())+"</code>\nChecking it now…")
		b.checkAndReport(ctx, r.Name)
	case "/rules", "/list":
		b.reply(ctx, b.rulesMessage())
	case "/remove", "/delete", "/pause", "/resume":
		if len(args) != 1 {
			b.reply(ctx, "Usage: "+cmd+" &lt;rule name&gt;")
			return false
		}
		name, err := b.changeRule(strings.TrimPrefix(cmd, "/"), args[0])
		if err != nil {
			b.reply(ctx, e(err.Error())+" /rules lists them.")
			return false
		}
		b.reply(ctx, "✅ "+strings.TrimPrefix(cmd, "/")+"d "+e(name))
	case "/check":
		if len(args) > 0 {
			var found bool
			b.withState(func(st *watch.State) { found = st.Rule(args[0]) != nil })
			if !found {
				b.reply(ctx, "No rule called "+e(args[0])+". /rules lists them.")
				return false
			}
			b.reply(ctx, "Checking "+e(args[0])+"…")
			b.checkAndReport(ctx, args[0])
			return false
		}
		b.reply(ctx, "Checking every rule…")
		b.checkAndReport(ctx, "")
		b.reply(ctx, "Done.")
		return true
	case "/phone":
		if len(args) != 1 {
			b.reply(ctx, "Usage: /phone &lt;advert id or URL&gt;")
			return false
		}
		id, err := advertID(args[0])
		if err != nil {
			b.reply(ctx, "❌ "+e(err.Error()))
			return false
		}
		// Asked for by a person, one advert at a time: the only way the
		// watch ever fetches a number. The connector has no such tool.
		b.mu.Lock()
		phone, err := b.src.Phone(ctx, id)
		b.mu.Unlock()
		if err != nil {
			b.reply(ctx, "❌ "+e(err.Error()))
			return false
		}
		b.reply(ctx, fmt.Sprintf("📞 %s (advert %d)", e(phone), id))
	case "/new":
		b.withState(func(st *watch.State) { st.Chat = watch.ChatRef{} })
		b.reply(ctx, "Started afresh: your next message begins a new conversation with Claude.")
	case "/start", "/help":
		b.reply(ctx, botHelp())
	default:
		b.reply(ctx, "I don't know that one. /help lists what I understand.")
	}
	return false
}

// changeRule pauses, resumes or removes the rule called name and returns
// its exact name.
func (b *bot) changeRule(op, name string) (string, error) {
	var found string
	b.withState(func(st *watch.State) {
		r := st.Rule(name)
		if r == nil {
			return
		}
		found = r.Name
		switch op {
		case "pause":
			r.Paused = true
		case "resume":
			r.Paused = false
		default:
			st.RemoveRule(found)
		}
	})
	if found == "" {
		return "", fmt.Errorf("no rule called %q", name)
	}
	return found, nil
}

// ruleInfo is a rule as /rules and the connector show it.
type ruleInfo struct {
	Rule    string `json:"rule"`
	Name    string `json:"name"`
	Paused  bool   `json:"paused"`
	Matched int    `json:"matched_so_far"`
}

func (b *bot) rules() []ruleInfo {
	var out []ruleInfo
	b.withState(func(st *watch.State) {
		for _, r := range st.Rules {
			matched := 0
			for _, seen := range st.Seen[r.Name] {
				if seen.Matched {
					matched++
				}
			}
			out = append(out, ruleInfo{Rule: r.String(), Name: r.Name, Paused: r.Paused, Matched: matched})
		}
	})
	return out
}

func (b *bot) rulesMessage() string {
	rules := b.rules()
	if len(rules) == 0 {
		return "No rules yet. /add one, or just tell me what you are looking for; /help shows how."
	}
	var s strings.Builder
	for _, r := range rules {
		state := "▶️"
		if r.Paused {
			state = "⏸"
		}
		fmt.Fprintf(&s, "%s <code>%s</code>\n   %d matched so far\n", state, html.EscapeString(r.Rule), r.Matched)
	}
	return s.String()
}

// chatPreamble opens every new conversation, since claudeweb's system prompt
// is its own.
const chatPreamble = `[I am writing from Telegram, through my unegui.mn watch bot (uneguictl). Use the unegui connector: search, show_advert, categories and filters look at unegui.mn, Mongolia's classifieds site; list_rules, set_rule, pause_rule, resume_rule, remove_rule and check_now manage the rules the bot watches. The bot sends every new match, and every price drop, to this Telegram chat by itself. Sellers' phone numbers are not available to you: tell me to tap /phone_<id> under an advert. Reply briefly, in the language I write in; Telegram shows your reply as plain text with **bold**, ` + "`code`" + ` and links, nothing more.]`

// talk sends a message that is not a command to Claude, in the claudeweb
// chat the last one went to, and relays the answer.
func (b *bot) talk(ctx context.Context, text string) {
	if b.claude == nil {
		b.reply(ctx, "Chatting with Claude is not set up here. /help lists the commands.")
		return
	}
	if b.tg != nil {
		defer b.tg.KeepTyping(ctx)()
	}
	var ref watch.ChatRef
	b.withState(func(st *watch.State) { ref = st.Chat })
	for attempt := 0; attempt < 2; attempt++ {
		prompt, fresh := text, ref.ID == ""
		if fresh {
			chat, err := b.claude.NewChat(ctx, "unegui.mn (Telegram)", b.model, []string{b.connector})
			if err != nil {
				b.reply(ctx, "⚠️ "+html.EscapeString(err.Error()))
				return
			}
			ref = watch.ChatRef{ID: chat.ID}
			prompt = chatPreamble + "\n\n" + text
		}
		r, err := b.claude.Send(ctx, ref.ID, ref.Parent, prompt)
		if err != nil && !fresh && strings.Contains(err.Error(), "answered 404") {
			// The chat was deleted in claudeweb: start a new one.
			ref = watch.ChatRef{}
			continue
		}
		if err != nil {
			msg := err.Error()
			if errors.Is(err, claudeweb.ErrBusy) {
				msg = "Claude is still answering your previous message; wait for it, then send this again."
			}
			b.reply(ctx, "⚠️ "+html.EscapeString(msg))
			return
		}
		ref.Parent = r.ID
		b.withState(func(st *watch.State) { st.Chat = ref })
		answer := r.Text
		if answer == "" {
			answer = "(Claude finished without writing anything.)"
		}
		chunks := splitMessage(answer, messageBudget)
		if fresh && b.chatURL != "" {
			chunks[len(chunks)-1] += "\n\n<i>Also in claudeweb: " + html.EscapeString(b.chatURL+"/chat/"+ref.ID) + "</i>"
		}
		for _, c := range chunks {
			b.reply(ctx, c)
		}
		return
	}
}

var (
	mdBoldRE = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	mdCodeRE = regexp.MustCompile("`([^`\n]+)`")
	mdLinkRE = regexp.MustCompile(`\[([^\]\n]+)\]\((https?://[^)\s]+)\)`)
	mdHeadRE = regexp.MustCompile(`(?m)^#{1,6} +(.+)$`)
)

// splitMessage turns Claude's Markdown into Telegram's HTML subset, in
// pieces of at most n characters cut at line ends.
func splitMessage(md string, n int) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, line := range strings.Split(md, "\n") {
		h := html.EscapeString(line)
		h = mdHeadRE.ReplaceAllString(h, "<b>$1</b>")
		h = mdBoldRE.ReplaceAllString(h, "<b>$1</b>")
		h = mdCodeRE.ReplaceAllString(h, "<code>$1</code>")
		h = mdLinkRE.ReplaceAllString(h, `<a href="$2">$1</a>`)
		if utf8.RuneCountInString(cur.String())+utf8.RuneCountInString(h)+1 > n {
			flush()
		}
		if utf8.RuneCountInString(h) > n {
			// One enormous line: send it plain, cut.
			out = append(out, escapeTruncate(line, n))
			continue
		}
		if cur.Len() > 0 {
			cur.WriteString("\n")
		}
		cur.WriteString(h)
	}
	flush()
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

// advertID takes an advert id, slug or URL.
func advertID(s string) (int64, error) {
	if id, err := strconv.ParseInt(s, 10, 64); err == nil && id > 0 {
		return id, nil
	}
	slug, err := unegui.AdvertSlug(s)
	if err != nil {
		return 0, err
	}
	idPart, _, _ := strings.Cut(slug, "_")
	return strconv.ParseInt(idPart, 10, 64)
}

func botHelp() string {
	return `I watch unegui.mn and send you every new advert that matches your rules, with its price, battery, features, seller, description and photo. When a match gets cheaper, I tell you.

💬 Anything that is not a command goes to Claude (Sonnet, in claudeweb), which can search unegui.mn and set up rules for you: "find a 15 Pro Max under 2.2m with original parts and watch for new ones". /new starts a fresh conversation.

/add &lt;rule&gt; — add a rule, or replace one with the same name
/rules — list the rules
/pause &lt;name&gt;, /resume &lt;name&gt;, /remove &lt;name&gt;
/check [name] — run now instead of waiting
/phone &lt;id&gt; — the seller's number (tap /phone_… under an advert). The seller sees it as a phone view.

A rule:
<pre>` + html.EscapeString(ruleHelp) + `</pre>
Example:
<code>/add promax category=utas--dugaar/gar-utas-ba-utasnuud/iphone/iphone-15-pro-max max=2.5m battery=90 cycles=100 original=yes</code>`
}
