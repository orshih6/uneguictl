package watch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Rule is one saved search the watch runs. Prices and filters are kept as
// written, the way `uneguictl search` takes them, and checked when the rule
// is added.
type Rule struct {
	Name     string   `json:"name"`
	Category string   `json:"category,omitempty"`
	Query    string   `json:"query,omitempty"`
	Min      string   `json:"min,omitempty"`
	Max      string   `json:"max,omitempty"`
	Filters  []string `json:"filters,omitempty"`
	// Match keeps only titles containing every word of Query.
	Match      bool `json:"match,omitempty"`
	NoPromoted bool `json:"no_promoted,omitempty"`
	// MinBattery and MaxCycles are read from the advert's text; see
	// Battery.Meets for adverts that do not say.
	MinBattery   int  `json:"min_battery,omitempty"`
	MaxCycles    int  `json:"max_cycles,omitempty"`
	KeepUnstated bool `json:"keep_unstated,omitempty"`
	// Original rejects adverts whose text says a part was replaced.
	Original bool `json:"original,omitempty"`
	// Include and Exclude are case-insensitive regular expressions over the
	// title and description: one of Include must match, none of Exclude.
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
	// Limit is how many of the newest adverts to look through.
	Limit  int  `json:"limit,omitempty"`
	Paused bool `json:"paused,omitempty"`
}

// String writes r back in the key=value form the bot's /add takes.
func (r Rule) String() string {
	parts := []string{r.Name}
	add := func(k, v string) {
		if v != "" && v != "0" && v != "false" {
			parts = append(parts, k+"="+v)
		}
	}
	add("category", r.Category)
	add("q", strings.ReplaceAll(r.Query, " ", "+"))
	add("min", r.Min)
	add("max", r.Max)
	for _, f := range r.Filters {
		add("filter", f)
	}
	add("match", strconv.FormatBool(r.Match))
	add("nopromoted", strconv.FormatBool(r.NoPromoted))
	add("battery", strconv.Itoa(r.MinBattery))
	add("cycles", strconv.Itoa(r.MaxCycles))
	add("unstated", strconv.FormatBool(r.KeepUnstated))
	add("original", strconv.FormatBool(r.Original))
	for _, s := range r.Include {
		add("include", s)
	}
	for _, s := range r.Exclude {
		add("exclude", s)
	}
	add("limit", strconv.Itoa(r.Limit))
	return strings.Join(parts, " ")
}

// State is everything the watch keeps between runs: the rules, every advert
// each rule has looked at (so each is fetched and announced once, and a
// price drop on a match is noticed), and where Telegram's updates were left.
type State struct {
	path  string
	Rules []Rule                     `json:"rules"`
	Seen  map[string]map[int64]*Seen `json:"seen"`
	// Offset is the next Telegram update to ask for.
	Offset int64 `json:"telegram_offset,omitempty"`
	// Chat is the claudeweb chat Telegram messages continue.
	Chat ChatRef `json:"claudeweb_chat,omitempty"`
}

// ChatRef is a claudeweb chat and the assistant message the next message
// follows.
type ChatRef struct {
	ID     string `json:"id,omitempty"`
	Parent string `json:"parent,omitempty"`
}

// Seen is one advert as a rule last saw it.
type Seen struct {
	Price int64 `json:"price"`
	// Matched is true when the advert passed the rule and was announced.
	Matched   bool      `json:"matched,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// Load reads the state file at path; a missing file is an empty state.
func Load(path string) (*State, error) {
	s := &State{path: path}
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal(b, s); err != nil {
			return nil, fmt.Errorf("state file %s: %w", path, err)
		}
	}
	if s.Seen == nil {
		s.Seen = map[string]map[int64]*Seen{}
	}
	return s, nil
}

// Save writes the state back, replacing the file in one step so a crash
// cannot leave half of it.
func (s *State) Save() error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".uneguictl-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

// Rule returns the rule called name, or nil.
func (s *State) Rule(name string) *Rule {
	for i := range s.Rules {
		if strings.EqualFold(s.Rules[i].Name, name) {
			return &s.Rules[i]
		}
	}
	return nil
}

// SetRule adds r, or replaces the rule of the same name. A replaced rule
// forgets what it saw, so its next run announces the current matches anew;
// sending the same rule again changes nothing.
func (s *State) SetRule(r Rule) {
	if old := s.Rule(r.Name); old != nil {
		if old.String() == r.String() && old.Name == r.Name {
			old.Paused = r.Paused
			return
		}
		delete(s.Seen, old.Name)
		*old = r
		return
	}
	s.Rules = append(s.Rules, r)
}

// RemoveRule deletes the rule called name and what it saw. It reports
// whether there was one.
func (s *State) RemoveRule(name string) bool {
	for i, r := range s.Rules {
		if strings.EqualFold(r.Name, name) {
			s.Rules = append(s.Rules[:i], s.Rules[i+1:]...)
			delete(s.Seen, r.Name)
			return true
		}
	}
	return false
}

// Started reports whether rule has run before, and marks it as having run.
func (s *State) Started(rule string) bool {
	if _, ok := s.Seen[rule]; ok {
		return true
	}
	s.Seen[rule] = map[int64]*Seen{}
	return false
}

// Get returns what rule last recorded about advert id.
func (s *State) Get(rule string, id int64) (*Seen, bool) {
	seen, ok := s.Seen[rule][id]
	return seen, ok
}

// Put records advert id for rule.
func (s *State) Put(rule string, id int64, seen *Seen) {
	if s.Seen[rule] == nil {
		s.Seen[rule] = map[int64]*Seen{}
	}
	s.Seen[rule][id] = seen
}

// Prune forgets rule's adverts not seen since before, so the file does not
// grow forever with adverts that were sold or removed.
func (s *State) Prune(rule string, before time.Time) {
	for id, seen := range s.Seen[rule] {
		if seen.LastSeen.Before(before) {
			delete(s.Seen[rule], id)
		}
	}
}
