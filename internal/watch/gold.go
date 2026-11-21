package watch

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"gitlab.com/niharokz/kronos/internal/job"
)

// watch_gold: today's gold rate per gram for one city and purity (default 22K).
// Tells you when the rate differs from the last stored one.
//
// Sources are tried in order; the first page with a rate wins. A rate from another site than
// last time is stored silently (sites differ by a few %, that is not a price change).
//
// options:
//   city:    (required)           {city} in the urls; kronos.yml takes it from .env: ${KRONOS_GOLD_CITY}
//   karat:   22                   {karat} in pattern (24, 22, 18)
//   urls:    [angelone, groww, cleartax pages]   tried in order
//   url:     one page instead of urls
//   pattern: regex on the page's visible text; group 1 is the rate. Default matches
//            "₹12,383.20 per gram for 22 karat gold" and "₹13,104 per gram for 22K gold".

func init() { Register(goldWatcher) }

var goldWatcher = Watcher[Gold]{
	Name:   "gold",
	Title:  "Gold rate",
	Notify: []string{Telegram, Phone},
	Fetch:  fetchGold,
	Show:   func(g Gold) string { return g.line() + sourceNote(g.Source) },
	Diff:   diffGold,
}

// Gold is the stored rate.
type Gold struct {
	City    string  `yaml:"city"`
	Karat   int     `yaml:"karat"`
	PerGram float64 `yaml:"per_gram"`         // ₹
	Source  string  `yaml:"source,omitempty"` // site the rate came from
}

// goldURLs are tried in order (cleartax went 410 and stale in Nov 2026, so it is last).
var goldURLs = []string{
	"https://www.angelone.in/gold-rates-today/gold-rate-in-{city}",
	"https://groww.in/gold-rates/gold-rate-today-in-{city}",
	"https://cleartax.in/gold-rate-{city}",
}

const goldPattern = `₹\s*([\d,]+(?:\.\d+)?)\s*per gram for {karat}\s*(?:K|karat|carat)\b`

func fetchGold(c *job.Ctx, h *Client) (Gold, []string, error) {
	g := Gold{City: strings.ToLower(strings.TrimSpace(c.String("city", ""))), Karat: c.Int("karat", 22)} // kronos.yml: jobs.watch_gold.options
	if g.City == "" {
		return g, nil, fmt.Errorf("options.city is empty – set KRONOS_GOLD_CITY in .env")
	}
	pat := strings.ReplaceAll(c.String("pattern", goldPattern), "{karat}", fmt.Sprint(g.Karat))
	re, err := regexp.Compile("(?i)" + pat)
	if err != nil {
		return g, nil, fmt.Errorf("options.pattern: %v", err)
	}
	urls := c.Strings("urls", goldURLs)
	if one := c.String("url", ""); one != "" {
		urls = []string{one}
	}
	var errs []string
	for _, u := range urls {
		u = strings.ReplaceAll(u, "{city}", strings.ReplaceAll(g.City, " ", "-"))
		rate, err := goldFrom(h, u, re)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		for _, e := range errs { // an earlier source failed: worth a line, not a failure
			c.Log.Warn("Skipped %s", e)
		}
		g.PerGram, g.Source = rate, host(u)
		return g, nil, nil
	}
	return g, nil, fmt.Errorf("no source had a rate: %s", strings.Join(errs, "; "))
}

// goldFrom reads one page.
func goldFrom(h *Client, u string, re *regexp.Regexp) (float64, error) {
	page, err := h.Get(u)
	if err != nil {
		return 0, err
	}
	rate, err := parseGold(page, re)
	if err != nil {
		return 0, fmt.Errorf("%s: %v", u, err)
	}
	return rate, nil
}

// parseGold finds the rate in the page's visible text.
func parseGold(page string, re *regexp.Regexp) (float64, error) {
	m := re.FindStringSubmatch(Text(page))
	if len(m) < 2 {
		return 0, fmt.Errorf("rate not found (page layout changed? adjust options.pattern)")
	}
	v, ok := Number(m[1])
	if !ok || v <= 0 {
		return 0, fmt.Errorf("rate %q is not a number", m[1])
	}
	return v, nil
}

func (g Gold) line() string {
	return fmt.Sprintf("%dK gold, %s: ₹%s per gram", g.Karat, title(g.City), Commas(g.PerGram))
}

func sourceNote(s string) string {
	if s == "" {
		return ""
	}
	return " (" + s + ")"
}

func diffGold(c *job.Ctx, old, cur Gold) string {
	if old.Karat != cur.Karat || old.City != cur.City { // config changed: just the new baseline
		return cur.line() + " (now watching this instead of " + old.line() + ")"
	}
	if old.Source != cur.Source { // another site: new baseline, no alert
		if c != nil {
			c.Log.Info("Rate now from %s (was %s): stored as the new baseline", cur.Source, orUnknown(old.Source))
		}
		return ""
	}
	if old.PerGram == cur.PerGram {
		return ""
	}
	d, arrow := cur.PerGram-old.PerGram, "▲"
	if d < 0 {
		d, arrow = -d, "▼"
	}
	return fmt.Sprintf("%s\n%s ₹%s from ₹%s (%.2f%%)", cur.line(), arrow, Commas(d), Commas(old.PerGram), d/old.PerGram*100)
}

func orUnknown(s string) string {
	if s == "" {
		return "an older Kronos"
	}
	return s
}

// host is "angelone.in" for "https://www.angelone.in/…".
func host(u string) string {
	p, err := url.Parse(u)
	if err != nil || p.Host == "" {
		return u
	}
	return strings.TrimPrefix(p.Hostname(), "www.")
}

// title capitalises a city name: "new delhi" → "New Delhi".
func title(s string) string {
	words := strings.Fields(strings.ReplaceAll(s, "-", " "))
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}
