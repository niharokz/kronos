package watch

import (
	"fmt"
	"regexp"
	"strings"

	"gitlab.com/niharokz/kronos/internal/job"
)

// watch_gold: today's gold rate per gram for one city and purity (default 22K, Bangalore).
// Tells you when the rate differs from the last stored one.
//
// options:
//   city:    bangalore            {city} in url
//   karat:   22                   {karat} in pattern (24, 22, 18)
//   url:     https://cleartax.in/gold-rate-{city}
//   pattern: regex on the page's visible text; group 1 is the rate. Default matches
//            "₹13,479 per gram for 22K gold". Change url + pattern to use another site.

func init() { Register(goldWatcher) }

var goldWatcher = Watcher[Gold]{
	Name:   "gold",
	Title:  "Gold rate",
	Notify: []string{Telegram, Rahi},
	Fetch:  fetchGold,
	Show:   func(g Gold) string { return g.line() },
	Diff:   diffGold,
}

// Gold is the stored rate.
type Gold struct {
	City    string  `yaml:"city"`
	Karat   int     `yaml:"karat"`
	PerGram float64 `yaml:"per_gram"` // ₹
}

const (
	goldURL     = "https://cleartax.in/gold-rate-{city}"
	goldPattern = `₹\s*([\d,]+(?:\.\d+)?)\s*per gram for {karat}\s*K gold`
)

func fetchGold(c *job.Ctx, h *Client) (Gold, []string, error) {
	g := Gold{City: strings.ToLower(c.String("city", "bangalore")), Karat: c.Int("karat", 22)} // kronos.yml: jobs.watch_gold.options
	url := strings.ReplaceAll(c.String("url", goldURL), "{city}", g.City)
	pat := strings.ReplaceAll(c.String("pattern", goldPattern), "{karat}", fmt.Sprint(g.Karat))
	re, err := regexp.Compile("(?i)" + pat)
	if err != nil {
		return g, nil, fmt.Errorf("options.pattern: %v", err)
	}
	page, err := h.Get(url)
	if err != nil {
		return g, nil, err
	}
	rate, err := parseGold(page, re)
	if err != nil {
		return g, nil, fmt.Errorf("%s: %v", url, err)
	}
	g.PerGram = rate
	return g, nil, nil
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

func diffGold(_ *job.Ctx, old, cur Gold) string {
	if old.Karat != cur.Karat || old.City != cur.City { // config changed: just the new baseline
		return cur.line() + " (now watching this instead of " + old.line() + ")"
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

// title capitalises a city name: "new delhi" → "New Delhi".
func title(s string) string {
	words := strings.Fields(strings.ReplaceAll(s, "-", " "))
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}
