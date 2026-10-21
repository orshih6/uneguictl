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
	"time"

	"github.com/spf13/cobra"

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
	state string
	every time.Duration
	once  bool
	rules []string
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
		r, err := parseRule(strings.Fields(s))
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
	return b.run(ctx)
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
	src   watchSource
	out   sender
	state *watch.State
	log   io.Writer
	pause time.Duration
	now   func() time.Time
}

// checkAll runs every rule that is not paused, saving after each. It goes
// on past a failing rule and returns the first error.
func (w *watcher) checkAll(ctx context.Context) error {
	var first error
	for _, r := range w.state.Rules {
		if r.Paused {
			continue
		}
		if err := w.check(ctx, r); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Fprintf(w.log, "rule %s: %v\n", r.Name, err)
			if first == nil {
				first = fmt.Errorf("rule %s: %w", r.Name, err)
			}
		}
	}
	return first
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
	// lastErr is the last check failure reported to the chat, so a
	// lasting one (Cloudflare, say) is reported once, not every run.
	lastErr string
}

func (b *bot) run(ctx context.Context) error {
	fmt.Fprintf(b.log, "watching %d rules every %s\n", len(b.state.Rules), b.every)
	next := time.Now()
	for {
		if !time.Now().Before(next) {
			b.checkAndReport(ctx, "")
			next = time.Now().Add(b.every)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		wait := time.Until(next)
		if wait > 50*time.Second {
			wait = 50 * time.Second
		}
		if wait < time.Second {
			wait = time.Second
		}
		msgs, err := b.tg.Updates(ctx, b.state.Offset, wait)
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
			b.state.Offset = m.UpdateID + 1
			b.save()
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
	} else if r := b.state.Rule(name); r != nil {
		err = b.check(ctx, *r)
		if err != nil {
			err = fmt.Errorf("rule %s: %w", r.Name, err)
		}
	}
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

func (b *bot) reply(ctx context.Context, text string) {
	if err := b.out.Send(ctx, text, ""); err != nil {
		fmt.Fprintf(b.log, "%v\n", err)
	}
}

var phoneCmdRE = regexp.MustCompile(`^/phone_(\d+)$`)

// handle acts on one command. It reports whether it ran every rule, so the
// next scheduled run can wait a full interval.
func (b *bot) handle(ctx context.Context, text string) (ranAll bool) {
	words := strings.Fields(text)
	if len(words) == 0 {
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
		r, err := parseRule(args)
		if err != nil {
			b.reply(ctx, "❌ "+e(err.Error())+"\n\n<pre>/add "+e(ruleHelp)+"</pre>")
			return false
		}
		b.state.SetRule(r)
		b.save()
		b.reply(ctx, "✅ Saved <code>"+e(r.String())+"</code>\nChecking it now…")
		b.checkAndReport(ctx, r.Name)
	case "/rules", "/list":
		if len(b.state.Rules) == 0 {
			b.reply(ctx, "No rules yet. /add one; /help shows how.")
			return false
		}
		var s strings.Builder
		for _, r := range b.state.Rules {
			matched := 0
			for _, seen := range b.state.Seen[r.Name] {
				if seen.Matched {
					matched++
				}
			}
			state := "▶️"
			if r.Paused {
				state = "⏸"
			}
			fmt.Fprintf(&s, "%s <code>%s</code>\n   %d matched so far\n", state, e(r.String()), matched)
		}
		b.reply(ctx, s.String())
	case "/remove", "/delete", "/pause", "/resume":
		if len(args) != 1 {
			b.reply(ctx, "Usage: "+cmd+" &lt;rule name&gt;")
			return false
		}
		r := b.state.Rule(args[0])
		if r == nil {
			b.reply(ctx, "No rule called "+e(args[0])+". /rules lists them.")
			return false
		}
		name := r.Name
		switch cmd {
		case "/pause":
			r.Paused = true
		case "/resume":
			r.Paused = false
		default:
			b.state.RemoveRule(name)
		}
		b.save()
		b.reply(ctx, "✅ "+strings.TrimPrefix(cmd, "/")+"d "+e(name))
	case "/check":
		if len(args) > 0 {
			if b.state.Rule(args[0]) == nil {
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
		// watch ever fetches a number.
		phone, err := b.src.Phone(ctx, id)
		if err != nil {
			b.reply(ctx, "❌ "+e(err.Error()))
			return false
		}
		b.reply(ctx, fmt.Sprintf("📞 %s (advert %d)", e(phone), id))
	case "/start", "/help":
		b.reply(ctx, botHelp())
	default:
		b.reply(ctx, "I don't know that one. /help lists what I understand.")
	}
	return false
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

/add &lt;rule&gt; — add a rule, or replace one with the same name
/rules — list the rules
/pause &lt;name&gt;, /resume &lt;name&gt;, /remove &lt;name&gt;
/check [name] — run now instead of waiting
/phone &lt;id&gt; — the seller's number (tap /phone_… under an advert). The seller sees it as a phone view.

A rule:
<pre>` + html.EscapeString(ruleHelp) + `</pre>
Example:
<code>/add promax category=utas--dugaar/gar-utas-ba-utasnuud/iphone/iphone-15-pro-max max=2.5m battery=90 cycles=100 exclude=solis|солис</code>`
}
