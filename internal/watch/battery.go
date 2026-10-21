// Package watch holds what `uneguictl watch` needs besides the site itself:
// reading battery figures out of advert text, remembering which adverts were
// already seen, and sending Telegram messages.
package watch

import (
	"regexp"
	"strconv"
	"strings"
)

// Battery is what an advert's text says about a phone's battery. Zero means
// the text does not say.
type Battery struct {
	// Health is the maximum capacity in percent, as iOS shows it.
	Health int `json:"health,omitempty"`
	Cycles int `json:"cycles,omitempty"`
}

// Sellers write the figures every way: "bh100", "BH-89%", "bh:86%",
// "Battery-91%", "battery health 88%tai", "Баттерей 89", "cc 50", "Cc986".
// Go's \b is ASCII-only, which is right for the Latin words and harmless
// for the Cyrillic ones.
var (
	healthRE = regexp.MustCompile(`(?:\bbh|\bbatt?err?y(?:\s*health[a-z]*)?|ба?тт?ер[еэ]й|батарей)\s*[:=\-–]?\s*(\d{2,3})\s*%?`)
	cyclesRE = regexp.MustCompile(`(?:\bcc|\bcycles?(?:\s*count)?|цикл)\s*[:=\-–]?\s*(\d{1,4})\b`)
)

// ParseBattery reads battery health and cycle count from an advert's title
// and description. A figure outside what a phone can show is ignored rather
// than trusted.
func ParseBattery(text string) Battery {
	text = strings.ToLower(text)
	var b Battery
	if m := healthRE.FindStringSubmatch(text); m != nil {
		if n, _ := strconv.Atoi(m[1]); n >= 50 && n <= 100 {
			b.Health = n
		}
	}
	if m := cyclesRE.FindStringSubmatch(text); m != nil {
		if n, _ := strconv.Atoi(m[1]); n <= 3000 {
			b.Cycles = n
		}
	}
	return b
}

// Meets reports whether b satisfies a minimum health and a maximum cycle
// count (zero means no bound). Every figure the seller gave must be within
// its bound, and at least one bounded figure must be given; when none is,
// the answer is keepUnstated.
func (b Battery) Meets(minHealth, maxCycles int, keepUnstated bool) bool {
	if minHealth == 0 && maxCycles == 0 {
		return true
	}
	stated := false
	if minHealth > 0 && b.Health > 0 {
		if b.Health < minHealth {
			return false
		}
		stated = true
	}
	if maxCycles > 0 && b.Cycles > 0 {
		if b.Cycles > maxCycles {
			return false
		}
		stated = true
	}
	return stated || keepUnstated
}

// String is e.g. "BH 100% · 50 cycles", or "" when nothing is stated.
func (b Battery) String() string {
	var parts []string
	if b.Health > 0 {
		parts = append(parts, "BH "+strconv.Itoa(b.Health)+"%")
	}
	if b.Cycles > 0 {
		parts = append(parts, strconv.Itoa(b.Cycles)+" cycles")
	}
	return strings.Join(parts, " · ")
}
