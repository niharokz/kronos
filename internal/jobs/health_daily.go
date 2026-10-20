package jobs

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"gitlab.com/niharokz/kronos/internal/job"
	"gitlab.com/niharokz/kronos/internal/store"
)

// Daily activity cards: steps, sleep stages, heart rate and calories,
// each drawn as plain inline SVG (no JS, no external assets, matching
// the rest of the "nss" page). health.md's daily entries are wearable
// sync data (Fitbit / Google Fit / Samsung Health) — one entry per day
// with a consistent shape — so charts read straight off entries[i] by
// index rather than re-matching dates per series.

const (
	chartW, chartH                         = 280.0, 86.0
	chartPadX, chartPadTop, chartPadBottom = 3.0, 5.0, 14.0
	dailyChartDays                         = 30 // charts show the latest 30 days; older entries stay in health.md
)

// validDailyEntries keeps daily log entries that are mappings with a
// parseable date, sorted oldest first; malformed or undated entries are
// skipped with a warning, like the other health.md lists.
func validDailyEntries(c *job.Ctx, list *yaml.Node) []*yaml.Node {
	var out []*yaml.Node
	for _, d := range items(list) {
		d = store.Resolve(d)
		if d.Kind != yaml.MappingNode {
			c.Log.Warn("Skipping malformed daily entry: %s", store.PyRepr(d))
			continue
		}
		if _, ok := hdate(store.Get(d, "date")); !ok {
			c.Log.Warn("Daily entry missing/unparseable 'date': %s", store.PyStr(d))
			continue
		}
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool {
		di, _ := hdate(store.Get(out[i], "date"))
		dj, _ := hdate(store.Get(out[j], "date"))
		return di.Before(dj)
	})
	return out
}

// dailyField reads a numeric field off a daily entry, 0 when missing or
// unparseable (wearable syncs log these together, so a true gap is rare
// and reads honestly as an empty bar rather than a break in the chart).
func dailyField(e *yaml.Node, field string) float64 {
	v, _ := hnumber(htext(store.Get(e, field)))
	return v
}

func dailyDate(e *yaml.Node) time.Time {
	d, _ := hdate(store.Get(e, "date"))
	return d
}

func dailyAverage(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals))
}

// formatNum prints v with a fixed number of decimals (0 for counts/bpm, 1 for hours).
func formatNum(v float64, decimals int) string {
	return strconv.FormatFloat(v, 'f', decimals, 64)
}

func axisLabels(entries []*yaml.Node) string {
	n := len(entries)
	return fmt.Sprintf(
		`<text x="%.1f" y="%.0f" font-size="8" fill="var(--nss-muted)">%s</text>`+
			`<text x="%.1f" y="%.0f" font-size="8" fill="var(--nss-muted)" text-anchor="end">%s</text>`,
		chartPadX, chartH-2, store.PyHTMLEscape(dailyDate(entries[0]).Format("Jan 2")),
		chartW-chartPadX, chartH-2, store.PyHTMLEscape(dailyDate(entries[n-1]).Format("Jan 2")))
}

// barChartSVG draws one series as bars against its own max, with a dashed
// line at the series average.
func barChartSVG(entries []*yaml.Node, field, color string) string {
	n := len(entries)
	if n == 0 {
		return ""
	}
	vals := make([]float64, n)
	maxV := 0.0
	for i, e := range entries {
		vals[i] = dailyField(e, field)
		maxV = math.Max(maxV, vals[i])
	}
	if maxV == 0 {
		return "" // no values: the card is skipped
	}
	plotH := chartH - chartPadTop - chartPadBottom
	gap := (chartW - chartPadX*2) / float64(n)
	barW := math.Max(gap*0.56, 2)

	var bars strings.Builder
	for i, v := range vals {
		x := chartPadX + float64(i)*gap + (gap-barW)/2
		h := v / maxV * plotH
		if h > 0 && h < 2 {
			h = 2
		}
		y := chartPadTop + (plotH - h)
		bars.WriteString(fmt.Sprintf(`<rect x="%.2f" y="%.2f" width="%.2f" height="%.2f" rx="2" fill="%s"/>`, x, y, barW, h, color))
	}

	avg := dailyAverage(vals)
	avgY := chartPadTop + plotH - (avg / maxV * plotH)
	avgLine := fmt.Sprintf(`<line x1="%.2f" y1="%.2f" x2="%.2f" y2="%.2f" stroke="var(--nss-muted)" stroke-width="1" stroke-dasharray="3 3" opacity=".7"/>`,
		chartPadX, avgY, chartW-chartPadX, avgY)

	return fmt.Sprintf(`<svg viewBox="0 0 %.0f %.0f" width="100%%" role="img" aria-label="trend over %d days, dashed line is the average">%s%s%s</svg>`,
		chartW, chartH, n, bars.String(), avgLine, axisLabels(entries))
}

// sleepStagesSVG stacks deep/rem/light/awake hours per night: deep and REM
// (the restorative stages) in the brand teals, light and awake receding
// into a darker teal and the muted gray.
func sleepStagesSVG(entries []*yaml.Node) string {
	n := len(entries)
	if n == 0 {
		return ""
	}
	type stage struct{ deep, rem, light, awake float64 }
	stages := make([]stage, n)
	maxTotal := 0.0
	has := false
	for i, e := range entries {
		s := stage{
			deep:  dailyField(e, "deep_h"),
			rem:   dailyField(e, "rem_h"),
			light: dailyField(e, "light_h"),
			awake: dailyField(e, "awake_h"),
		}
		stages[i] = s
		total := s.deep + s.rem + s.light + s.awake
		if total > 0 {
			has = true
		}
		maxTotal = math.Max(maxTotal, total)
	}
	if !has {
		return ""
	}

	plotH := chartH - chartPadTop - chartPadBottom
	gap := (chartW - chartPadX*2) / float64(n)
	barW := math.Max(gap*0.56, 2)
	colors := [4]string{"var(--nss-accent-hi)", "var(--nss-accent)", "var(--nss-chart-3)", "var(--nss-muted)"}

	var bars strings.Builder
	for i, s := range stages {
		x := chartPadX + float64(i)*gap + (gap-barW)/2
		y := chartPadTop + plotH
		for seg, v := range [4]float64{s.deep, s.rem, s.light, s.awake} {
			h := v / maxTotal * plotH
			y -= h
			if h <= 0 {
				continue
			}
			bars.WriteString(fmt.Sprintf(`<rect x="%.2f" y="%.2f" width="%.2f" height="%.2f" fill="%s"/>`, x, y, barW, h, colors[seg]))
		}
	}

	return fmt.Sprintf(`<svg viewBox="0 0 %.0f %.0f" width="100%%" role="img" aria-label="sleep stages over %d nights">%s%s</svg>`,
		chartW, chartH, n, bars.String(), axisLabels(entries))
}

// sleepLegend labels the four sleep-stage colors.
func sleepLegend() string {
	return `<p><b style="color:var(--nss-accent-hi)">&#9679;</b> deep &nbsp;` +
		`<b style="color:var(--nss-accent)">&#9679;</b> REM &nbsp;` +
		`<b style="color:var(--nss-chart-3)">&#9679;</b> light &nbsp;` +
		`<b style="color:var(--nss-muted)">&#9679;</b> awake</p>`
}

// linePt is one present value of a line-chart series, keyed to its day's
// position so a missing reading leaves a gap instead of a false zero.
type linePt struct {
	x int
	v float64
}

func fieldLine(entries []*yaml.Node, field string) []linePt {
	var pts []linePt
	for i, e := range entries {
		if v, ok := hnumber(htext(store.Get(e, field))); ok {
			pts = append(pts, linePt{x: i, v: v})
		}
	}
	return pts
}

type lineSeries struct {
	pts   []linePt
	color string
}

func lineChartSVG(n int, series []lineSeries, entries []*yaml.Node) string {
	if n == 0 {
		return ""
	}
	plotH := chartH - chartPadTop - chartPadBottom
	plotW := chartW - chartPadX*2
	minV, maxV := math.Inf(1), math.Inf(-1)
	has := false
	for _, s := range series {
		for _, p := range s.pts {
			has = true
			minV = math.Min(minV, p.v)
			maxV = math.Max(maxV, p.v)
		}
	}
	if !has {
		return ""
	}
	if maxV == minV {
		maxV = minV + 1
	}
	xAt := func(i int) float64 {
		if n == 1 {
			return chartPadX + plotW/2
		}
		return chartPadX + float64(i)/float64(n-1)*plotW
	}
	yAt := func(v float64) float64 { return chartPadTop + plotH - (v-minV)/(maxV-minV)*plotH }

	var out strings.Builder
	for _, s := range series {
		if len(s.pts) == 0 {
			continue
		}
		var path strings.Builder
		for i, p := range s.pts {
			cmd := "L"
			if i == 0 {
				cmd = "M"
			}
			fmt.Fprintf(&path, "%s%.2f,%.2f ", cmd, xAt(p.x), yAt(p.v))
		}
		out.WriteString(fmt.Sprintf(`<path d="%s" fill="none" stroke="%s" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/>`,
			strings.TrimSpace(path.String()), s.color))
		for _, p := range s.pts {
			out.WriteString(fmt.Sprintf(`<circle cx="%.2f" cy="%.2f" r="1.8" fill="%s"/>`, xAt(p.x), yAt(p.v), s.color))
		}
	}
	out.WriteString(axisLabels(entries))

	return fmt.Sprintf(`<svg viewBox="0 0 %.0f %.0f" width="100%%" role="img" aria-label="trend over %d days">%s</svg>`, chartW, chartH, n, out.String())
}

// heartRateSVG draws resting/average/max heart rate as three lines,
// calmest to most intense: muted, accent, accent-hi.
func heartRateSVG(entries []*yaml.Node) string {
	return lineChartSVG(len(entries), []lineSeries{
		{pts: fieldLine(entries, "hr_rest"), color: "var(--nss-muted)"},
		{pts: fieldLine(entries, "hr_avg"), color: "var(--nss-accent)"},
		{pts: fieldLine(entries, "hr_max"), color: "var(--nss-accent-hi)"},
	}, entries)
}

// hrLegend labels the three heart-rate lines with a colored dot each.
func hrLegend() string {
	return `<p><b style="color:var(--nss-muted)">&#9679;</b> resting &nbsp;` +
		`<b style="color:var(--nss-accent)">&#9679;</b> average &nbsp;` +
		`<b style="color:var(--nss-accent-hi)">&#9679;</b> max</p>`
}

// caloriesSVG draws active vs. total calories as paired bars per day:
// active (accent) sits inside total (muted), both against the same scale.
func caloriesSVG(entries []*yaml.Node) string {
	n := len(entries)
	if n == 0 {
		return ""
	}
	totals := make([]float64, n)
	actives := make([]float64, n)
	maxV := 0.0
	for i, e := range entries {
		totals[i] = dailyField(e, "kcal_total")
		actives[i] = dailyField(e, "kcal_active")
		maxV = math.Max(maxV, math.Max(totals[i], actives[i]))
	}
	if maxV == 0 {
		return "" // no values: the card is skipped
	}
	plotH := chartH - chartPadTop - chartPadBottom
	gap := (chartW - chartPadX*2) / float64(n)
	barW := math.Max(gap*0.56, 2)

	var bars strings.Builder
	for i := range totals {
		x := chartPadX + float64(i)*gap + (gap-barW)/2
		hTotal := totals[i] / maxV * plotH
		hActive := actives[i] / maxV * plotH
		bars.WriteString(fmt.Sprintf(`<rect x="%.2f" y="%.2f" width="%.2f" height="%.2f" rx="2" fill="var(--nss-muted)" opacity=".45"/>`,
			x, chartPadTop+(plotH-hTotal), barW, hTotal))
		if hActive > 0 {
			bars.WriteString(fmt.Sprintf(`<rect x="%.2f" y="%.2f" width="%.2f" height="%.2f" rx="2" fill="var(--nss-accent)"/>`,
				x, chartPadTop+(plotH-hActive), barW, hActive))
		}
	}

	return fmt.Sprintf(`<svg viewBox="0 0 %.0f %.0f" width="100%%" role="img" aria-label="active vs total calories over %d days">%s%s</svg>`,
		chartW, chartH, n, bars.String(), axisLabels(entries))
}

// calLegend labels the active-vs-total calorie bars.
func calLegend() string {
	return `<p><b style="color:var(--nss-accent)">&#9679;</b> active &nbsp;` +
		`<b style="color:var(--nss-muted)">&#9679;</b> total</p>`
}

// renderDailyCards builds the Daily activity cards (steps, sleep, heart rate,
// calories) from the latest dailyChartDays entries; a card with no values is skipped.
func renderDailyCards(entries []*yaml.Node) string {
	if len(entries) == 0 {
		return ""
	}
	if len(entries) > dailyChartDays {
		entries = entries[len(entries)-dailyChartDays:]
	}
	last := entries[len(entries)-1]
	on := " on " + dailyDate(last).Format("Jan 2") // headline values are from the latest entry

	card := func(title, headline, chart, legend string) string {
		if chart == "" {
			return ""
		}
		return `
      <article>
        <header><h3>` + store.PyHTMLEscape(title) + `</h3><p>` + store.PyHTMLEscape(headline) + `</p></header>
        ` + chart + legend + `
      </article>`
	}

	var out strings.Builder
	out.WriteString(card("Steps", formatNum(dailyField(last, "steps"), 0)+" steps"+on,
		barChartSVG(entries, "steps", "var(--nss-accent)"), ""))
	out.WriteString(card("Sleep", formatNum(dailyField(last, "sleep_h"), 1)+"h sleep"+on,
		sleepStagesSVG(entries), sleepLegend()))
	out.WriteString(card("Heart rate", formatNum(dailyField(last, "hr_rest"), 0)+" bpm resting"+on,
		heartRateSVG(entries), hrLegend()))
	out.WriteString(card("Calories", formatNum(dailyField(last, "kcal_total"), 0)+" kcal"+on,
		caloriesSVG(entries), calLegend()))
	return out.String()
}
