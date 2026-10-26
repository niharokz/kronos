package watch

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gitlab.com/niharokz/kronos/internal/job"
)

// watch_domains: the cheapest registrars per TLD (port of domwatch.py), from domainoffer.net
// and tld-list.com. Tells you when the cheapest registrar or its prices change.
//
// options:
//   tlds:      [ar]                       TLDs to watch, without the dot
//   rank_by:   total                      total = transfer + 1st renewal | transfer | renew | register
//   top:       1                          registrars to keep per TLD
//   sources:   [domainoffer, tld-list]    drop tld-list if it keeps returning 403
//   max_pages: 5                          domainoffer pages per TLD (10 registrars each)

func init() { Register(domainsWatcher) }

var domainsWatcher = Watcher[Domains]{
	Name:   "domains",
	Title:  "Domain prices",
	Notify: []string{Telegram, Rahi},
	Fetch:  fetchDomains,
	Show:   showDomains,
	Diff:   diffDomains,
}

// Domains is the stored ranking: TLD → cheapest offers (USD).
type Domains struct {
	RankBy string             `yaml:"rank_by"`
	TLDs   map[string][]Offer `yaml:"tlds"`
}

// Offer is one registrar's prices for a TLD.
type Offer struct {
	Registrar string   `yaml:"registrar"`
	Score     float64  `yaml:"score"` // the rank_by value
	Transfer  *float64 `yaml:"transfer"`
	Renew     *float64 `yaml:"renew"`
	Register  *float64 `yaml:"register"`
	Link      string   `yaml:"link"`
}

var rankLabels = map[string]string{
	"total": "cheapest transfer + 1st renewal", "transfer": "cheapest transfer",
	"renew": "cheapest renewal", "register": "cheapest registration",
}

// Same registrar, different spellings across the two sites.
var registrarAliases = map[string]string{
	"marcariainternational": "marcaria", "marcariacom": "marcaria", "cloudnsnet": "cloudns", "subregcz": "subreg",
}

func fetchDomains(c *job.Ctx, h *Client) (Domains, []string, error) {
	rankBy := c.String("rank_by", "total") // kronos.yml: jobs.watch_domains.options
	if rankLabels[rankBy] == "" {
		return Domains{}, nil, fmt.Errorf("options.rank_by %q: use total, transfer, renew or register", rankBy)
	}
	top, pages := c.Int("top", 1), c.Int("max_pages", 5)
	sources := c.Strings("sources", []string{"domainoffer", "tld-list"})
	d := Domains{RankBy: rankBy, TLDs: map[string][]Offer{}}
	for _, tld := range c.Strings("tlds", []string{"ar"}) {
		tld = strings.ToLower(strings.TrimPrefix(tld, "."))
		var rows []Offer
		for _, src := range sources {
			got, err := scrapeSource(h, src, tld, pages)
			if err != nil { // the other source still counts; a broken one is only a warning
				c.Log.Warn("%s .%s: %v", src, tld, err)
			}
			rows = append(rows, got...)
		}
		best := rankOffers(rows, rankBy, top)
		if len(best) == 0 {
			return d, nil, fmt.Errorf(".%s: no prices found (sites down or layout changed)", tld)
		}
		d.TLDs[tld] = best
	}
	return d, nil, nil
}

func scrapeSource(h *Client, src, tld string, pages int) ([]Offer, error) {
	switch src {
	case "domainoffer":
		var all []Offer
		for p := 1; p <= pages; p++ {
			page, err := h.Get(fmt.Sprintf("https://domainoffer.net/tld/%s?page=%d", tld, p))
			if err != nil {
				return all, err
			}
			got, next := parseDomainOffer(page)
			all = append(all, got...)
			if len(got) == 0 || !next {
				break
			}
		}
		return all, nil
	case "tld-list":
		page, err := h.Get("https://tld-list.com/tld/" + tld)
		if err != nil {
			return nil, err
		}
		return parseTLDList(page), nil
	}
	return nil, fmt.Errorf("unknown source (use domainoffer, tld-list)")
}

var (
	reRow    = regexp.MustCompile(`(?is)<tr\b[^>]*>(.*?)</tr>`)
	reCell   = regexp.MustCompile(`(?is)<t([dh])\b([^>]*)>(.*?)</t[dh]>`)
	reLink   = regexp.MustCompile(`(?is)<a\b([^>]*)>(.*?)</a>`)
	reName   = regexp.MustCompile(`(?is)<[a-z0-9]+\b[^>]*class="[^"]*\bregistrar-name\b[^"]*"[^>]*>(.*?)</`)
	reTable  = regexp.MustCompile(`(?is)<table\b.*?</table>`)
	reDollar = regexp.MustCompile(`\$\s*([\d,]+(?:\.\d+)?)`)
)

type cell struct{ attrs, inner string }

func cells(row string) []cell {
	var out []cell
	for _, m := range reCell.FindAllStringSubmatch(row, -1) {
		out = append(out, cell{m[2], m[3]})
	}
	return out
}

// parseDomainOffer reads the #domain-list table (name, new, [icann fee], renew, transfer)
// and whether there is a next page.
func parseDomainOffer(page string) ([]Offer, bool) {
	i := strings.Index(page, `id="domain-list"`)
	if i < 0 {
		return nil, false
	}
	table := page[i:]
	if j := strings.Index(table, "</table>"); j >= 0 {
		table = table[:j]
	}
	var out []Offer
	for _, r := range reRow.FindAllStringSubmatch(table, -1) {
		var tds []cell
		for _, c := range cells(r[1]) {
			if !strings.Contains(Attr(c.attrs, "class"), "col-icann-fee") {
				tds = append(tds, c)
			}
		}
		if len(tds) < 4 {
			continue
		}
		name := reName.FindStringSubmatch(tds[0].inner)
		link := reLink.FindStringSubmatch(tds[0].inner)
		if name == nil || link == nil {
			continue
		}
		slug := strings.TrimRight(Attr(link[1], "href"), "/")
		slug = slug[strings.LastIndex(slug, "/")+1:]
		out = append(out, Offer{Registrar: Text(name[1]), Register: dollars(tds[1].inner),
			Renew: dollars(tds[2].inner), Transfer: dollars(tds[3].inner), Link: "https://domainoffer.net/go/" + slug})
	}
	next := false
	for _, a := range reLink.FindAllStringSubmatch(page, -1) {
		if strings.HasPrefix(strings.ToLower(Text(a[2])), "next") {
			next = true
		}
	}
	return out, next
}

// parseTLDList reads the first table whose header has Registration, Renewal and Transfer.
func parseTLDList(page string) []Offer {
	for _, table := range reTable.FindAllString(page, -1) {
		col := map[string]int{}
		rows := reRow.FindAllStringSubmatch(table, -1)
		if len(rows) == 0 {
			continue
		}
		for i, c := range cells(rows[0][1]) {
			col[strings.ToLower(Text(c.inner))] = i
		}
		reg, ok1 := col["registration"]
		ren, ok2 := col["renewal"]
		tra, ok3 := col["transfer"]
		if !ok1 || !ok2 || !ok3 {
			continue
		}
		var out []Offer
		for _, r := range rows[1:] {
			tds := cells(r[1])
			if len(tds) <= max(reg, ren, tra) {
				continue
			}
			name := Text(tds[0].inner)
			out = append(out, Offer{Registrar: name, Register: dollars(tds[reg].inner), Renew: dollars(tds[ren].inner),
				Transfer: dollars(tds[tra].inner), Link: "https://tld-list.com/registrars/" + registrarKey(name)})
		}
		return out
	}
	return nil
}

// dollars is the last $ amount in a cell ("€ →$72.52" → 72.52); nil if none.
func dollars(s string) *float64 {
	all := reDollar.FindAllStringSubmatch(Text(s), -1)
	if len(all) == 0 {
		return nil
	}
	v, ok := Number(all[len(all)-1][1])
	if !ok {
		return nil
	}
	return &v
}

func registrarKey(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	if a, ok := registrarAliases[b.String()]; ok {
		return a
	}
	return b.String()
}

// score is the value ranked on; false when a needed price is missing.
func score(o Offer, rankBy string) (float64, bool) {
	get := map[string]*float64{"transfer": o.Transfer, "renew": o.Renew, "register": o.Register}
	if rankBy == "total" {
		if o.Transfer == nil || o.Renew == nil {
			return 0, false
		}
		return round2(*o.Transfer + *o.Renew), true
	}
	if v := get[rankBy]; v != nil {
		return *v, true
	}
	return 0, false
}

// rankOffers keeps each registrar's best quote and returns the cheapest `top`.
func rankOffers(rows []Offer, rankBy string, top int) []Offer {
	best := map[string]Offer{}
	for _, o := range rows {
		s, ok := score(o, rankBy)
		if !ok {
			continue
		}
		o.Score = s
		k := registrarKey(o.Registrar)
		if b, seen := best[k]; !seen || s < b.Score || (s == b.Score && strings.Contains(o.Link, "domainoffer")) {
			best[k] = o
		}
	}
	out := make([]Offer, 0, len(best))
	for _, o := range best {
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score < out[j].Score
		}
		return strings.ToLower(out[i].Registrar) < strings.ToLower(out[j].Registrar)
	})
	if top > 0 && len(out) > top {
		out = out[:top]
	}
	return out
}

func showDomains(d Domains) string {
	var b strings.Builder
	for i, tld := range sortedKeys(d.TLDs) {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, ".%s — %s (USD)", tld, rankLabels[d.RankBy])
		for n, o := range d.TLDs[tld] {
			fmt.Fprintf(&b, "\n%d. %s: %s\n   %s", n+1, o.Registrar, o.prices(d.RankBy), o.Link)
		}
	}
	return b.String()
}

func (o Offer) prices(rankBy string) string {
	switch rankBy {
	case "total":
		return fmt.Sprintf("transfer %s + renew %s = %s", usd(o.Transfer), usd(o.Renew), usd(&o.Score))
	case "register":
		return fmt.Sprintf("register %s, renew %s", usd(o.Register), usd(o.Renew))
	}
	return fmt.Sprintf("transfer %s, renew %s", usd(o.Transfer), usd(o.Renew))
}

// diffDomains reports TLDs whose ranking changed (registrar or prices).
func diffDomains(_ *job.Ctx, old, cur Domains) string {
	var parts []string
	for _, tld := range sortedKeys(cur.TLDs) {
		was, now := old.TLDs[tld], cur.TLDs[tld]
		if old.RankBy == cur.RankBy && same(was, now) {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, ".%s — %s (USD)", tld, rankLabels[cur.RankBy])
		for n, o := range now {
			fmt.Fprintf(&b, "\n%d. %s: %s", n+1, o.Registrar, o.prices(cur.RankBy))
			if n < len(was) && old.RankBy == cur.RankBy && !same(was[n], o) {
				fmt.Fprintf(&b, "\n   was %s: %s", was[n].Registrar, usd(&was[n].Score))
			}
			fmt.Fprintf(&b, "\n   %s", o.Link)
		}
		parts = append(parts, b.String())
	}
	return strings.Join(parts, "\n\n")
}

func usd(v *float64) string {
	if v == nil {
		return "n/a"
	}
	return fmt.Sprintf("$%.2f", *v)
}

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
