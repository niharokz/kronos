package jobs

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"gitlab.com/niharokz/kronos/internal/job"
	"gitlab.com/niharokz/kronos/internal/store"
)

// health_website renders health.md into the static health diary PWA in
// paths.health_site: index.html, manifest.json, sw.js, icon.svg.
//
// The page template, service worker and icon in assets/ were extracted
// verbatim from Taskmaster's health_website.py, and the rendering below
// follows it line for line, so the output is the same page.

func init() { job.Register("health_website", healthWebsite) }

var (
	//go:embed assets/health_template.html
	healthTemplate string
	//go:embed assets/health_sw.js
	healthServiceWorker string
	//go:embed assets/health_icon.svg
	healthIcon string
)

const dash = "—" // shown for missing values

func healthWebsite(c *job.Ctx) error {
	path := c.Path("health") // kronos.yml: paths.health
	data, err := store.LoadMapping(path, true)
	if err != nil {
		return err
	}
	if len(data.Content) == 0 {
		return fmt.Errorf("%s is empty", filepath.Base(path))
	}

	meta := store.Get(data, "meta")
	if meta == nil || meta.Kind != yaml.MappingNode {
		c.Log.Warn("'meta' block missing or malformed, using defaults")
		meta = store.NewMapping()
	}
	runs := store.Get(data, "runs")
	if !store.Truthy(runs) {
		runs = store.NewMapping()
	} else if runs.Kind != yaml.MappingNode {
		c.Log.Warn("'runs' block malformed, ignoring")
		runs = store.NewMapping()
	}

	planned := validRuns(c, store.Get(runs, "planned"), "planned")
	recent := validRuns(c, store.Get(runs, "recent"), "recent")
	var gym []*yaml.Node
	for _, d := range items(store.Get(data, "gym")) {
		if g := validGymDay(c, d); g != nil {
			gym = append(gym, g)
		}
	}

	page := renderHealthPage(c, meta, planned, recent, gym)
	manifest, err := healthManifest(meta)
	if err != nil {
		return err
	}

	out := c.Path("health_site") // kronos.yml: paths.health_site (under .env WEB_PATH)
	if err := c.MkdirAll(out); err != nil {
		return err
	}
	for name, content := range map[string]string{
		"index.html":    page,
		"manifest.json": manifest,
		"sw.js":         healthServiceWorker,
		"icon.svg":      healthIcon,
	} {
		if err := c.WriteFile(filepath.Join(out, name), []byte(content)); err != nil {
			return err
		}
	}

	c.Log.OK("Health site written to %s (planned=%d, recent=%d, gym=%d)", out, len(planned), len(recent), len(gym))
	return nil
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
		if len(r.Content) > 0 {
			out = append(out, r)
		}
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
	if len(d.Content) == 0 {
		return nil
	}
	return d
}

// ── Rendering (same markup as the Python version) ───────────────────────

// esc is html.escape(str(value)), with "—" for a missing value or "".
func esc(n *yaml.Node) string {
	n = store.Resolve(n)
	if store.IsNull(n) || (n.Kind == yaml.ScalarNode && n.ShortTag() == "!!str" && n.Value == "") {
		return dash
	}
	return store.PyHTMLEscape(store.PyStr(n))
}

// escStr is esc for a Go string.
func escStr(s string) string {
	if s == "" {
		return dash
	}
	return store.PyHTMLEscape(s)
}

// sval is str(d.get(key) or "").strip() — "" for missing/null.
func sval(m *yaml.Node, key string) string {
	n := store.Get(m, key)
	if store.IsNull(n) {
		return ""
	}
	return strings.TrimSpace(store.PyStr(n))
}

// orDefault is `d.get(key) or default` rendered as a string.
func orDefault(m *yaml.Node, key, def string) *yaml.Node {
	if n := store.Get(m, key); store.Truthy(n) {
		return n
	}
	return store.Str(def)
}

func renderGymCard(d *yaml.Node) string {
	style := ""
	if store.Truthy(store.Get(d, "full_width")) {
		style = ` style="grid-column: 1 / -1;"`
	}
	chips := make([]string, 0)
	for _, ex := range items(store.Get(d, "exercises")) {
		chips = append(chips, `<span class="chip">`+esc(ex)+`</span>`)
	}
	return `          <article class="gym-card"` + style + `>
            <span class="card-emoji" aria-hidden="true">` + "\U0001F3CB️" + `</span>
            <div class="day">` + esc(store.Get(d, "day")) + `</div>
            <p>
              <b>` + esc(store.Get(d, "title")) + `</b><br>
              ` + strings.Join(chips, "\n              ") + `
            </p>
          </article>`
}

func renderRunCard(r *yaml.Node, emoji string) string {
	link := sval(r, "link")
	name := escStr(dash) // Python: esc(run.get("name", "—"))
	if store.Has(r, "name") {
		name = esc(store.Get(r, "name"))
	}
	heading := name
	if link != "" {
		heading = `<a href="` + escStr(link) + `" target="_blank" rel="noopener noreferrer" class="run-link">` + name + " ↗</a>"
	}

	var meta []string
	for i := 0; i+1 < len(r.Content); i += 2 {
		k := store.PyStr(r.Content[i])
		if k == "name" || k == "link" {
			continue
		}
		reward := ""
		if k == "reward" {
			reward = "reward"
		}
		meta = append(meta, `<div class="meta-item"><div class="label">`+escStr(strings.ToUpper(k))+`</div>`+
			`<div class="value `+reward+`">`+esc(r.Content[i+1])+`</div></div>`)
	}
	return `            <article class="card">
              <span class="card-emoji" aria-hidden="true">` + emoji + `</span>
              <h3>` + heading + `</h3>
              <div class="meta">
                ` + strings.Join(meta, "\n                ") + `
              </div>
            </article>`
}

// calcBMI is weight(kg) / height(m)², rounded to one decimal; "" if invalid.
func calcBMI(weight, height string) string {
	w, err1 := strconv.ParseFloat(strings.TrimSpace(strings.ReplaceAll(strings.ToLower(weight), "kg", "")), 64)
	h, err2 := strconv.ParseFloat(strings.TrimSpace(strings.ReplaceAll(strings.ToLower(height), "cm", "")), 64)
	if err1 != nil || err2 != nil {
		return ""
	}
	h /= 100
	if h <= 0 {
		return ""
	}
	rounded, _ := strconv.ParseFloat(strconv.FormatFloat(w/(h*h), 'f', 1, 64), 64)
	return store.PyFloat(rounded)
}

func renderPills(c *job.Ctx, meta *yaml.Node) string {
	var pills []string
	last := sval(meta, "last_updated")
	if last == "" {
		last = c.Now.Format("02 Jan 2006")
	}
	pills = append(pills, `<span class="pill" title="Last updated">`+"\U0001F553 "+escStr(last)+`</span>`)

	weight := sval(meta, "weight")
	if weight != "" {
		pills = append(pills, `<span class="pill" title="Weight">`+"⚖️ "+escStr(weight)+`</span>`)
	}
	height := sval(meta, "height")
	if height != "" {
		pills = append(pills, `<span class="pill" title="Height">`+"\U0001F4CF "+escStr(height)+`</span>`)
	}
	bmi := sval(meta, "bmi")
	if bmi == "" && weight != "" && height != "" {
		bmi = calcBMI(weight, height)
	}
	if bmi != "" {
		pills = append(pills, `<span class="pill" title="Body Mass Index">`+"\U0001F4CA BMI "+escStr(bmi)+`</span>`)
	}
	if bf := sval(meta, "body_fat"); bf != "" {
		pills = append(pills, `<span class="pill" title="Body fat">`+"\U0001F9EC BF "+escStr(bf)+`%</span>`)
	}
	return strings.Join(pills, "\n")
}

func renderHealthPage(c *job.Ctx, meta *yaml.Node, planned, recent, gym []*yaml.Node) string {
	description := sval(meta, "subtitle")
	if description == "" {
		description = "A personal run + gym progress diary."
	}

	cards := func(list []*yaml.Node, render func(*yaml.Node) string, empty string) string {
		var parts []string
		for _, n := range list {
			if s := render(n); s != "" {
				parts = append(parts, s)
			}
		}
		if len(parts) == 0 {
			return empty
		}
		return strings.Join(parts, "\n")
	}
	planned_ := cards(planned, func(r *yaml.Node) string { return renderRunCard(r, "\U0001F4C5") },
		`<p class="empty-state">No planned runs yet.</p>`)
	recent_ := cards(recent, func(r *yaml.Node) string { return renderRunCard(r, "\U0001F3C3‍♂️") },
		`<p class="empty-state">No recent runs logged yet.</p>`)
	gym_ := cards(gym, renderGymCard, `<p class="empty-state">No gym schedule set.</p>`)

	// Same replacement order as the Python version.
	page := healthTemplate
	for _, r := range [][2]string{
		{"{{TITLE}}", esc(orDefault(meta, "title", "Health Diary"))},
		{"{{SUBTITLE}}", esc(orDefault(meta, "subtitle", "Personal training log"))},
		{"{{TAGLINE}}", esc(orDefault(meta, "tagline", "RUN • GYM • PROGRESS"))},
		{"{{FOOTER}}", esc(orDefault(meta, "footer", ""))},
		{"{{DESCRIPTION}}", escStr(description)},
		{"{{PILLS}}", renderPills(c, meta)},
		{"{{PLANNED_CARDS}}", planned_},
		{"{{RECENT_CARDS}}", recent_},
		{"{{GYM_CARDS}}", gym_},
	} {
		page = strings.ReplaceAll(page, r[0], r[1])
	}
	return page
}

// healthManifest is the web app manifest, formatted exactly like Python's
// json.dumps(indent=2) (non-ASCII as \uXXXX).
func healthManifest(meta *yaml.Node) (string, error) {
	name := sval(meta, "title")
	if name == "" {
		name = "Health Diary"
	}
	short := name
	if utf8.RuneCountInString(name) > 12 {
		short = "Health"
	}
	tagline := sval(meta, "tagline")
	if tagline == "" {
		tagline = "Run • Gym • Progress"
	}

	type icon struct {
		Src     string `json:"src"`
		Sizes   string `json:"sizes"`
		Type    string `json:"type"`
		Purpose string `json:"purpose"`
	}
	m := struct {
		Name            string `json:"name"`
		ShortName       string `json:"short_name"`
		Description     string `json:"description"`
		StartURL        string `json:"start_url"`
		Scope           string `json:"scope"`
		Display         string `json:"display"`
		Orientation     string `json:"orientation"`
		BackgroundColor string `json:"background_color"`
		ThemeColor      string `json:"theme_color"`
		Icons           []icon `json:"icons"`
	}{name, short, tagline, "./index.html", "./", "standalone", "portrait-primary", "#03050a", "#00e5a0",
		[]icon{{"icon.svg", "any", "image/svg+xml", "any maskable"}}}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return "", err
	}
	return asciiJSON(strings.TrimRight(buf.String(), "\n")), nil
}

// asciiJSON escapes non-ASCII as \uXXXX (surrogate pairs above U+FFFF),
// matching Python's json.dumps default ensure_ascii=True.
func asciiJSON(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x80:
			b.WriteRune(r)
		case r <= 0xFFFF:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			r -= 0x10000
			fmt.Fprintf(&b, `\u%04x\u%04x`, 0xD800+(r>>10), 0xDC00+(r&0x3FF))
		}
	}
	return b.String()
}
