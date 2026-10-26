package watch

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gitlab.com/niharokz/kronos/internal/job"
)

// watch_rewards: the public Microsoft Rewards redeem catalog (port of rewards_watch.py).
// Tells you about new items and points changes; a title or text change alone is stored
// silently. A page that fails to load keeps its old items (nothing "disappears").
//
// options:
//   market:         in                                  /redeem/<market>/…
//   pages:          [featured, giftcards, donate, sweepstakes]
//   notify_price:   true                                points changes
//   notify_removed: false                               items gone from the catalog

func init() { Register(rewardsWatcher) }

var rewardsWatcher = Watcher[Catalog]{
	Name:   "rewards",
	Title:  "Microsoft Rewards",
	Notify: []string{Telegram},
	Fetch:  fetchRewards,
	Show:   showRewards,
	Diff:   diffRewards,
	Keep:   keepRewards,
}

// Catalog is the stored list, sorted by SKU.
type Catalog struct {
	Items []Item `yaml:"items"`
}

// Item is one redeemable item.
type Item struct {
	SKU      string `yaml:"sku"`
	Title    string `yaml:"title"`
	Points   int    `yaml:"points"` // 0 = not shown
	Category string `yaml:"category"`
	Desc     string `yaml:"desc,omitempty"`
}

func (it Item) url() string { return "https://rewards.bing.com/redeem/sku/" + it.SKU }

var rewardsPages = map[string]string{"featured": "", "giftcards": "/shop", "donate": "/donate", "sweepstakes": "/win"}

var (
	reSKUAnchor = regexp.MustCompile(`(?is)<a\b([^>]*\bhref\s*=\s*["'][^"']*/redeem/sku/([A-Za-z0-9_-]+)[^>]*)>(.*?)</a>`)
	reImgAlt    = regexp.MustCompile(`(?is)<img\b[^>]*\balt\s*=\s*"([^"]+)"`)
	reHeading   = regexp.MustCompile(`(?is)<h[1-6]\b[^>]*>(.*?)</h[1-6]>`)
	rePoints    = regexp.MustCompile(`(?i)([\d,]+)\s*(?:pts|points)\b`)
)

func fetchRewards(c *job.Ctx, h *Client) (Catalog, []string, error) {
	market := c.String("market", "in") // kronos.yml: jobs.watch_rewards.options
	items := map[string]Item{}
	var warns []string
	ok := 0
	for _, cat := range c.Strings("pages", []string{"featured", "giftcards", "donate", "sweepstakes"}) {
		suffix, known := rewardsPages[cat]
		if !known {
			return Catalog{}, nil, fmt.Errorf("options.pages: unknown page %q (featured, giftcards, donate, sweepstakes)", cat)
		}
		page, err := h.Get("https://rewards.bing.com/redeem/" + market + suffix)
		if err != nil {
			warns = append(warns, fmt.Sprintf("%s: %v", cat, err))
			continue
		}
		ok++
		for _, it := range parseRewards(page, cat) {
			if prev, seen := items[it.SKU]; !seen || len(it.Title) > len(prev.Title) {
				if seen {
					it.Category = prev.Category // first tab it appeared on
				}
				items[it.SKU] = it
			}
		}
	}
	if ok == 0 {
		return Catalog{}, nil, fmt.Errorf("no catalog page loaded: %s", strings.Join(warns, "; "))
	}
	if len(items) == 0 { // never replace a good list with an empty or blocked response
		return Catalog{}, nil, fmt.Errorf("0 items found (layout change or blocked?)")
	}
	return sortedCatalog(items), warns, nil
}

// parseRewards reads every /redeem/sku/<id> card on one page.
func parseRewards(page, cat string) []Item {
	var out []Item
	for _, m := range reSKUAnchor.FindAllStringSubmatch(page, -1) {
		inner, text := m[3], Text(m[3])
		it := Item{SKU: m[2], Category: cat}
		switch {
		case reImgAlt.MatchString(inner):
			it.Title = Text(reImgAlt.FindStringSubmatch(inner)[1])
		case reHeading.MatchString(inner):
			it.Title = Text(reHeading.FindStringSubmatch(inner)[1])
		default:
			it.Title = firstN(rePoints.ReplaceAllString(text, ""), 80)
		}
		if half := len(it.Title) / 2; half > 4 && strings.TrimSpace(it.Title[:half]) == strings.TrimSpace(it.Title[half:]) {
			it.Title = strings.TrimSpace(it.Title[:half]) // cards often repeat the title
		}
		if p := rePoints.FindStringSubmatch(text); p != nil {
			if v, ok := Number(p[1]); ok {
				it.Points = int(v)
			}
		}
		desc := strings.Replace(text, it.Title, "", 2)
		it.Desc = firstN(strings.TrimSpace(reSpace.ReplaceAllString(rePoints.ReplaceAllString(desc, ""), " ")), 200)
		if it.Title == "" {
			it.Title = it.SKU
		}
		out = append(out, it)
	}
	return out
}

func sortedCatalog(m map[string]Item) Catalog {
	var c Catalog
	for _, it := range m {
		c.Items = append(c.Items, it)
	}
	sort.Slice(c.Items, func(i, j int) bool { return c.Items[i].SKU < c.Items[j].SKU })
	return c
}

// keepRewards: after a partial fetch, items from the pages that failed stay.
func keepRewards(old, cur Catalog) Catalog {
	m := map[string]Item{}
	for _, it := range old.Items {
		m[it.SKU] = it
	}
	for _, it := range cur.Items {
		m[it.SKU] = it
	}
	return sortedCatalog(m)
}

func showRewards(c Catalog) string {
	lines := []string{fmt.Sprintf("%d items in the catalog", len(c.Items))}
	for _, it := range c.Items {
		lines = append(lines, "• "+it.line())
	}
	return strings.Join(lines, "\n")
}

func (it Item) line() string {
	pts := "? pts"
	if it.Points > 0 {
		pts = Commas(float64(it.Points)) + " pts"
	}
	return fmt.Sprintf("%s — %s [%s]", it.Title, pts, it.Category)
}

// diffRewards: new items always; points changes and removals per options.
func diffRewards(c *job.Ctx, old, cur Catalog) string {
	was := map[string]Item{}
	for _, it := range old.Items {
		was[it.SKU] = it
	}
	now := map[string]bool{}
	var added, priced, removed []string
	for _, it := range cur.Items {
		now[it.SKU] = true
		o, seen := was[it.SKU]
		switch {
		case !seen:
			s := "🆕 " + it.line()
			if it.Desc != "" {
				s += "\n" + it.Desc
			}
			added = append(added, s+"\n"+it.url())
		case it.Points > 0 && o.Points != it.Points:
			priced = append(priced, fmt.Sprintf("%s: %s → %s pts\n%s", it.Title, Commas(float64(o.Points)), Commas(float64(it.Points)), it.url()))
		}
	}
	for _, it := range old.Items {
		if !now[it.SKU] {
			removed = append(removed, "➖ "+it.Title)
		}
	}
	var parts []string
	if len(added) > 0 {
		parts = append(parts, "New items\n\n"+strings.Join(added, "\n\n"))
	}
	if len(priced) > 0 && c.Bool("notify_price", true) { // kronos.yml: jobs.watch_rewards.options.notify_price
		parts = append(parts, "Points changed\n\n"+strings.Join(priced, "\n\n"))
	}
	if len(removed) > 0 && c.Bool("notify_removed", false) { // kronos.yml: jobs.watch_rewards.options.notify_removed
		parts = append(parts, "Removed\n\n"+strings.Join(removed, "\n"))
	}
	return strings.Join(parts, "\n\n")
}

func firstN(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return strings.TrimSpace(string(r[:n]))
	}
	return s
}
