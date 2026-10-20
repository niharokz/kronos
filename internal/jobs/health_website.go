package jobs

import (
	_ "embed"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"gitlab.com/niharokz/kronos/internal/dates"
	"gitlab.com/niharokz/kronos/internal/job"
	"gitlab.com/niharokz/kronos/internal/store"
)

// health_website renders health.md into the health diary page
// (paths.health_site/index.html) in the "nss" look: teal accent,
// square-grid background, dark by default and light by system setting;
// classless (bare semantic tags), no JavaScript, no tracking, no external
// fonts or CDNs.
//
// A line-for-line port of health_website.py (nss edition). Computed extras
// come from data already in health.md: a stats strip (races, distance, best
// 10K, money spent, BMI), pace per run (time ÷ distance) and a countdown on
// planned runs.

func init() { job.Register("health_website", healthWebsite) }

//go:embed assets/health.css
var healthCSS string // the nss stylesheet: tokens + element selectors only

const dash = "—" // shown for missing values

// Run fields never shown as label/value rows (handled elsewhere or internal).
var skipFields = map[string]bool{"id": true, "name": true, "link": true, "date": true}

const healthLogo = `<svg viewBox="0 0 48 48" width="44" height="44" aria-hidden="true">
        <rect width="48" height="48" rx="10" fill="#3a807a"/>
        <polyline points="8,26 16,26 20,15 27,34 31,21 35,26 40,26" fill="none" stroke="#fff"
                  stroke-width="3.5" stroke-linecap="round" stroke-linejoin="round"/>
      </svg>`

const healthFaviconSVG = "<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 48 48'>" +
	"<rect width='48' height='48' rx='10' fill='#3a807a'/>" +
	"<polyline points='8,26 16,26 20,15 27,34 31,21 35,26 40,26' fill='none' " +
	"stroke='#fff' stroke-width='3.5' stroke-linecap='round' stroke-linejoin='round'/></svg>"

func healthWebsite(c *job.Ctx) error {
	path := c.Path("health") // kronos.yml: paths.health
	data, err := store.LoadMapping(path, true)
	if err != nil {
		return err
	}
	if len(data.Content) == 0 {
		return fmt.Errorf("%s is empty", filepath.Base(path))
	}

	meta := mappingOr(c, store.Get(data, "meta"), "meta")
	runs := mappingOr(c, store.Get(data, "runs"), "runs")

	planned := validRuns(c, store.Get(runs, "planned"), "planned")
	recent := validRuns(c, store.Get(runs, "recent"), "recent")
	var gym []*yaml.Node
	for _, d := range items(store.Get(data, "gym")) {
		if g := validGymDay(c, d); g != nil {
			gym = append(gym, g)
		}
	}
	daily := validDailyEntries(c, store.Get(data, "daily"))

	page := renderHealthPage(c.Today, meta, planned, recent, gym, daily)

	out := c.Path("health_site") // kronos.yml: paths.health_site (under .env WEB_PATH)
	if err := c.MkdirAll(out); err != nil {
		return err
	}
	if err := c.WriteFile(filepath.Join(out, "index.html"), []byte(page)); err != nil {
		return err
	}
	c.Log.OK("Health site written to %s (planned=%d, recent=%d, gym=%d, daily=%d)", out, len(planned), len(recent), len(gym), len(daily))
	return nil
}

// ── Loading and validation ──────────────────────────────────────────────

// mappingOr returns n when it is a mapping; a missing value or a
// malformed one becomes an empty mapping (the latter with a warning).
func mappingOr(c *job.Ctx, n *yaml.Node, name string) *yaml.Node {
	switch {
	case !store.Truthy(n):
		return store.NewMapping()
	case n.Kind != yaml.MappingNode:
		c.Log.Warn("'%s' block malformed, ignoring", name)
		return store.NewMapping()
	}
	return n
}

// items returns the entries of a list value (Python iterates `x or []`).
func items(n *yaml.Node) []*yaml.Node {
	n = store.Resolve(n)
	if !store.Truthy(n) || n.Kind != yaml.SequenceNode {
		return nil
	}
	return n.Content
}

func validRuns(c *job.Ctx, list *yaml.Node, kind string) []*yaml.Node {
	var out []*yaml.Node
	for _, r := range items(list) {
		r = store.Resolve(r)
		if r.Kind != yaml.MappingNode {
			c.Log.Warn("Skipping malformed %s run entry: %s", kind, store.PyRepr(r))
			continue
		}
		for _, field := range []string{"name", "date"} {
			if !store.Truthy(store.Get(r, field)) {
				c.Log.Warn("%s run missing '%s': %s", kind, field, store.PyStr(r))
				store.Set(r, field, store.Str(dash))
			}
		}
		out = append(out, r)
	}
	return out
}

func validGymDay(c *job.Ctx, d *yaml.Node) *yaml.Node {
	d = store.Resolve(d)
	if d.Kind != yaml.MappingNode {
		c.Log.Warn("Skipping malformed gym day entry: %s", store.PyRepr(d))
		return nil
	}
	for _, field := range []string{"day", "title"} {
		if !store.Truthy(store.Get(d, field)) {
			store.Set(d, field, store.Str(dash))
		}
	}
	if ex := store.Get(d, "exercises"); ex == nil || ex.Kind != yaml.SequenceNode {
		store.Set(d, "exercises", store.Seq())
	}
	return d
}

// ── Value helpers (YAML can give str, int, float, date, null) ───────────

// htext is the plain display text: null → "", 2500.0 → "2500", lists
// joined with ", ", strings trimmed.
func htext(n *yaml.Node) string {
	n = store.Resolve(n)
	if store.IsNull(n) {
		return ""
	}
	if n.Kind == yaml.SequenceNode {
		parts := make([]string, len(n.Content))
		for i, c := range n.Content {
			parts[i] = htext(c)
		}
		return strings.Join(parts, ", ")
	}
	if n.Kind == yaml.ScalarNode && n.ShortTag() == "!!float" {
		if f, err := strconv.ParseFloat(strings.ReplaceAll(n.Value, "_", ""), 64); err == nil &&
			!math.IsInf(f, 0) && f == math.Trunc(f) {
			return strconv.FormatFloat(f, 'f', 0, 64)
		}
	}
	return strings.TrimSpace(store.PyStr(n))
}

// hesc is html-escaped display text, "—" when empty.
func hesc(n *yaml.Node) string {
	if s := store.PyHTMLEscape(htext(n)); s != "" {
		return s
	}
	return dash
}

// orNode is Python's `value or default`.
func orNode(n *yaml.Node, def string) *yaml.Node {
	if store.Truthy(n) {
		return n
	}
	return store.Str(def)
}

var firstNumber = regexp.MustCompile(`\d+(?:\.\d+)?`)

// hnumber is the first number in a value ("67kg" → 67).
func hnumber(s string) (float64, bool) {
	m := firstNumber.FindString(s)
	if m == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(m, 64)
	return f, err == nil
}

// hdate parses a run date: an unquoted YAML date, or "25 Oct 2026",
// "25 October 2026", "2026-10-25", "25-10-2026".
func hdate(n *yaml.Node) (time.Time, bool) {
	n = store.Resolve(n)
	if n != nil && n.Kind == yaml.ScalarNode && n.ShortTag() == "!!timestamp" {
		return dates.ParseTimestamp(n.Value)
	}
	s := htext(n)
	for _, layout := range []string{"2 Jan 2006", "2 January 2006", "2006-1-2", "2-1-2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return dates.New(t.Year(), t.Month(), t.Day()), true
		}
	}
	return time.Time{}, false
}

var kmPattern = regexp.MustCompile(`(?i)^\s*(\d+(?:\.\d+)?)\s*(k|km)?\s*$`)

// parseKm reads "10K", "3.46K", "10 km" as kilometres.
func parseKm(n *yaml.Node) (float64, bool) {
	m := kmPattern.FindStringSubmatch(htext(n))
	if m == nil {
		return 0, false
	}
	f, err := strconv.ParseFloat(m[1], 64)
	return f, err == nil
}

// parseMinutes reads "52:00" → 52, "107:00" → 107, "1:04:30" → 64.5.
func parseMinutes(n *yaml.Node) (float64, bool) {
	parts := strings.Split(htext(n), ":")
	nums := make([]float64, len(parts))
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return 0, false
		}
		nums[i] = f
	}
	switch len(nums) {
	case 2:
		return nums[0] + nums[1]/60, true
	case 3:
		return nums[0]*60 + nums[1] + nums[2]/60, true
	}
	return 0, false
}

// fmtClock formats minutes as m:ss (64.5 → "64:30").
func fmtClock(minutes float64) string {
	total := int(math.RoundToEven(minutes * 60))
	return fmt.Sprintf("%d:%02d", total/60, total%60)
}

// rupees formats ₹1,234 (whole rupees, thousands separators).
func rupees(n float64) string {
	s := strconv.FormatFloat(math.Abs(n), 'f', 0, 64)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if n < 0 {
		s = "-" + s
	}
	return "₹" + s
}

// fmtMoney shows a cost: 0 → "Free", 2500 → "₹2,500", text otherwise.
func fmtMoney(n *yaml.Node) string {
	v, ok := hnumber(htext(n))
	switch {
	case !ok:
		return htext(n)
	case v == 0:
		return "Free"
	}
	return rupees(v)
}

var hasScheme = regexp.MustCompile(`(?i)^https?://`)

// splitLink: "example.com" → ("https://example.com", "");
// "https://example.org/race  ID:1234" → ("https://example.org/race", "ID:1234").
func splitLink(n *yaml.Node) (string, string) {
	fields := strings.Fields(htext(n))
	if len(fields) == 0 {
		return "", ""
	}
	url := fields[0]
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(htext(n)), url))
	if !hasScheme.MatchString(url) {
		url = "https://" + url
	}
	return url, rest
}

// countdown is "Today", "Tomorrow", "In 19 days" or "3 days ago".
func countdown(d, today time.Time) string {
	days := int(d.Sub(today).Hours() / 24)
	switch {
	case days == 0:
		return "Today"
	case days == 1:
		return "Tomorrow"
	case days > 1:
		return fmt.Sprintf("In %d days", days)
	}
	return fmt.Sprintf("%d days ago", -days)
}

// byDate sorts runs by date; unparseable dates always go last.
func byDate(runs []*yaml.Node, newestFirst bool) []*yaml.Node {
	out := append([]*yaml.Node(nil), runs...)
	key := func(r *yaml.Node) (int, int64) {
		d, ok := hdate(store.Get(r, "date"))
		if !ok {
			return 1, 0
		}
		if newestFirst {
			return 0, -d.Unix()
		}
		return 0, d.Unix()
	}
	sort.SliceStable(out, func(i, j int) bool {
		ai, ad := key(out[i])
		bi, bd := key(out[j])
		if ai != bi {
			return ai < bi
		}
		return ad < bd
	})
	return out
}

// ── Rendering (bare semantic tags only: no class attributes, no JS) ─────

var exercisePattern = regexp.MustCompile(`^(.*?)\s+(\d+\s*x\s*\S+)$`)

func renderRun(r *yaml.Node, planned bool, today time.Time) string {
	name := hesc(store.Get(r, "name"))
	url, linkNote := splitLink(store.Get(r, "link"))
	heading := name
	if url != "" {
		heading = `<a href="` + store.PyHTMLEscape(url) + `" rel="noopener">` + name + " ↗</a>"
	}

	d, hasDate := hdate(store.Get(r, "date"))
	km, hasKm := parseKm(store.Get(r, "distance"))
	mins, hasMins := parseMinutes(store.Get(r, "time"))

	var rows strings.Builder
	for i := 0; i+1 < len(r.Content); i += 2 {
		keyNode, value := r.Content[i], r.Content[i+1]
		key := store.PyStr(keyNode)
		isStr := keyNode.ShortTag() == "!!str"
		if (isStr && skipFields[key]) || htext(value) == "" {
			continue
		}
		label := store.PyHTMLEscape(strings.ReplaceAll(key, "_", " "))
		var shown string
		switch {
		case isStr && key == "cost":
			shown = store.PyHTMLEscape(fmtMoney(value))
		case isStr && key == "reward":
			shown = "<mark>" + hesc(value) + "</mark>"
		default:
			shown = hesc(value)
		}
		rows.WriteString("<div><dt>" + label + "</dt><dd>" + shown + "</dd></div>")
		// computed pace goes right after time
		if isStr && key == "time" && hasKm && km != 0 && hasMins && mins != 0 {
			rows.WriteString("<div><dt>pace</dt><dd>" + fmtClock(mins/km) + " /km</dd></div>")
		}
	}
	if linkNote != "" {
		rows.WriteString("<div><dt>ref</dt><dd>" + store.PyHTMLEscape(linkNote) + "</dd></div>")
	}

	when := "<p>" + hesc(store.Get(r, "date")) + "</p>"
	lead := ""
	if planned && hasDate {
		lead = "<p>" + countdown(d, today) + "</p>"
	}

	return `
      <article>
        <header><h3>` + heading + `</h3>` + when + `</header>
        ` + lead + `
        <dl>` + rows.String() + `</dl>
      </article>`
}

func renderGym(d *yaml.Node) string {
	style := ""
	if store.Truthy(store.Get(d, "full_width")) {
		style = ` style="grid-column:1/-1"`
	}
	var list strings.Builder
	for _, ex := range items(store.Get(d, "exercises")) {
		s := htext(ex)
		if m := exercisePattern.FindStringSubmatch(s); m != nil {
			list.WriteString("<li><span>" + store.PyHTMLEscape(m[1]) + "</span><code>" + store.PyHTMLEscape(m[2]) + "</code></li>")
		} else if s != "" {
			list.WriteString("<li><span>" + store.PyHTMLEscape(s) + "</span></li>")
		}
	}
	body := "<p>Rest</p>"
	if list.Len() > 0 {
		body = "<ol>" + list.String() + "</ol>"
	}
	return `
      <article` + style + `>
        <header><h3>` + hesc(store.Get(d, "title")) + `</h3><p>` + hesc(store.Get(d, "day")) + `</p></header>
        ` + body + `
      </article>`
}

func renderStats(meta *yaml.Node, recent []*yaml.Node) string {
	var tiles strings.Builder
	tile := func(label, value string) {
		if value != "" {
			tiles.WriteString("<div><dt>" + label + "</dt><dd>" + store.PyHTMLEscape(value) + "</dd></div>")
		}
	}

	// Race totals, computed from recent runs
	totalKm, spent := 0.0, 0.0
	var tens []float64
	for _, r := range recent {
		if k, ok := parseKm(store.Get(r, "distance")); ok {
			totalKm += k
			if k == 10 {
				if t, ok := parseMinutes(store.Get(r, "time")); ok && t != 0 {
					tens = append(tens, t)
				}
			}
		}
		if n, ok := hnumber(htext(store.Get(r, "cost"))); ok {
			spent += n
		}
	}

	if len(recent) > 0 {
		tile("Races", strconv.Itoa(len(recent)))
	}
	if totalKm != 0 {
		tile("Distance", strconv.FormatFloat(totalKm, 'g', 6, 64)+" km")
	}
	if len(tens) > 0 {
		best := tens[0]
		for _, t := range tens[1:] {
			best = math.Min(best, t)
		}
		tile("Best 10K", fmtClock(best))
	}
	if spent != 0 {
		tile("Spent", rupees(spent))
	}

	// Body metrics
	weight, height := htext(store.Get(meta, "weight")), htext(store.Get(meta, "height"))
	tile("Weight", weight)
	tile("Height", height)

	bmi := htext(store.Get(meta, "bmi"))
	w, okW := hnumber(weight)
	h, okH := hnumber(height)
	if bmi == "" && okW && w != 0 && okH && h != 0 {
		m := h / 100
		bmi = strconv.FormatFloat(w/(m*m), 'f', 1, 64)
	}
	tile("BMI", bmi)

	bf := htext(store.Get(meta, "body_fat"))
	if bf != "" && !strings.HasSuffix(bf, "%") {
		bf += "%"
	}
	tile("Body fat", bf)

	if tiles.Len() == 0 {
		return ""
	}
	return "<dl>" + tiles.String() + "</dl>"
}

func healthSection(id, title, note, cards, empty string) string {
	if cards == "" {
		cards = "<p>" + empty + "</p>"
	}
	return `
    <section id="` + id + `">
      <h2>` + title + ` <small>` + note + `</small></h2>
      ` + cards + `
    </section>`
}

// healthFavicon is the logo as an inline data: URI (Python urllib quote()).
func healthFavicon() string {
	const safe = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_.-~/"
	var b strings.Builder
	for _, c := range []byte(healthFaviconSVG) {
		if strings.IndexByte(safe, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return "data:image/svg+xml," + b.String()
}

func renderHealthPage(today time.Time, meta *yaml.Node, planned, recent, gym, daily []*yaml.Node) string {
	title := hesc(orNode(store.Get(meta, "title"), "Health Diary"))
	subtitle := hesc(orNode(store.Get(meta, "subtitle"), "Run • Lift • Improve"))
	tagline := store.PyHTMLEscape(htext(store.Get(meta, "tagline")))
	footer := store.PyHTMLEscape(htext(store.Get(meta, "footer")))
	updated := htext(store.Get(meta, "last_updated"))
	if updated == "" {
		updated = today.Format("02 Jan 2006")
	}
	updated = store.PyHTMLEscape(updated)

	planned = byDate(planned, false)
	recent = byDate(recent, true)

	var plannedHTML, recentHTML, gymHTML strings.Builder
	for _, r := range planned {
		plannedHTML.WriteString(renderRun(r, true, today))
	}
	for _, r := range recent {
		recentHTML.WriteString(renderRun(r, false, today))
	}
	for _, d := range gym {
		gymHTML.WriteString(renderGym(d))
	}

	taglineHTML := ""
	if tagline != "" {
		taglineHTML = "<p>" + tagline + "</p>"
	}

	return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="description" content="` + subtitle + `">
  <meta name="color-scheme" content="dark light">
  <meta name="theme-color" content="#3a807a">
  <link rel="icon" type="image/svg+xml" href="` + healthFavicon() + `">
  <title>` + title + `</title>
  <style>` + healthCSS + `</style>
</head>
<body>
  <header>
    ` + healthLogo + `
    <div>
      <h1>` + title + `</h1>
      <p>` + subtitle + `</p>
      ` + taglineHTML + `
    </div>
    <nav>
      <a href="#stats">Stats</a>
      <a href="#daily">Daily</a>
      <a href="#planned">Planned</a>
      <a href="#recent">Recent</a>
      <a href="#gym">Gym</a>
    </nav>
  </header>

  <main>
    ` + healthSection("stats", "Stats", "at a glance", renderStats(meta, recent), "No stats yet.") + `
    ` + healthSection("daily", "Daily activity", strconv.Itoa(len(daily))+" days logged", renderDailyCards(daily), "No daily log yet.") + `
    ` + healthSection("planned", "Planned", strconv.Itoa(len(planned)), plannedHTML.String(), "No planned runs yet.") + `
    ` + healthSection("recent", "Recent runs", strconv.Itoa(len(recent)), recentHTML.String(), "No recent runs logged yet.") + `
    ` + healthSection("gym", "Gym schedule", strconv.Itoa(len(gym))+" days", gymHTML.String(), "No gym schedule set.") + `
  </main>

  <footer>
    <span>` + footer + `</span>
    <span>Updated ` + updated + `</span>
  </footer>
</body>
</html>
`
}
