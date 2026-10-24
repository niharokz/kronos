package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"gitlab.com/niharokz/kronos/internal/dates"
	"gitlab.com/niharokz/kronos/internal/job"
	"gitlab.com/niharokz/kronos/internal/pulse"
)

// pulse_rollup processes every Rahi day that has turned final and isn't rolled up yet:
// baselines (7/28-day medians, weekday/weekend), goals (start, weekly tighten/loosen),
// pattern warnings, weekly (when Sunday is final) and monthly (when the last day is final)
// rollups and reports. Every run also refreshes state/brief.yml for the morning briefing.
// Idempotent: reruns change nothing new.
//
// options: briefing_at ('05:40') — when nimo_briefing runs; later warnings wait for the next one

func init() { job.Register("pulse_rollup", pulseRollup) }

func pulseRollup(c *job.Ctx) error {
	dir := c.Path("rahi") // kronos.yml: paths.rahi
	g, err := pulseGoals(c, dir)
	if err != nil { // your file: warn and retry, no hourly alert storm
		c.Log.Warn("goals.md: %v – fix the YAML; retried next run", err)
		return nil
	}
	today, hhmm := c.Today, c.Now.Format("15:04")
	st, err := pulse.LoadState(pulse.BaselineFile(dir))
	if err != nil {
		c.Log.Warn("baseline.md: %v – retried next run", err)
		return nil
	}
	sent, err := pulse.LoadSent(pulse.SentFile(dir))
	if err != nil {
		c.Log.Warn("sent.yml: %v – retried next run", err)
		return nil
	}
	logs, err := pulse.LoadLogs(dir, today.AddDate(0, 0, -70), today)
	if err != nil {
		return err
	}
	for _, p := range logs.Problems {
		c.Log.Warn("%s", p)
	}
	final := logs.Final()
	metrics := append(g.GoalMetrics(), pulse.Metric{Key: pulse.KeyUnlocks})
	names := logs.AppNames()

	// 1. Each newly final day, oldest first: baselines, goals, warnings, rollups.
	var warnings []pulse.Message
	rolled := 0
	for _, d := range final {
		if st.Rolled(d.Date) {
			continue
		}
		st.Recompute(final, metrics, d.Date)
		st.StartGoals(g, final, d.Date, pulse.ISOWeek(d.Date))
		if st.Ready() && !d.Date.Before(today.AddDate(0, 0, -2)) { // after downtime, only recent days warn
			warnings = append(warnings, patternWarnings(g, st, logs, d.Date, names)...)
		}
		if d.Date.Weekday() == time.Sunday {
			if err := weeklyRollup(c, dir, g, st, final, d.Date, names); err != nil {
				return err
			}
		}
		if d.Date.AddDate(0, 0, 1).Day() == 1 {
			if err := monthlyRollup(c, dir, g, st, final, d.Date); err != nil {
				return err
			}
		}
		st.RolledDays = append(st.RolledDays, dates.Format(d.Date))
		rolled++
	}
	if n := len(final); n > 0 { // picks up apps newly added to tracked_apps
		st.Recompute(final, metrics, final[n-1].Date)
		st.StartGoals(g, final, final[n-1].Date, pulse.ISOWeek(final[n-1].Date))
	}
	st.RolledDays = lastN(st.RolledDays, 120)
	st.RolledWeeks, st.RolledMonths = lastN(st.RolledWeeks, 60), lastN(st.RolledMonths, 24)
	if err := saveState(c, dir, st); err != nil {
		return err
	}

	// 2. Pattern warnings → Telegram now, or the next briefing (never dropped).
	warnings = latestPerMetric(warnings, sent, today)
	quiet := g.Quiet(hhmm)
	send, deferred := pulse.Plan(warnings, sent, today, g.MaxMessages, quiet)
	nextBrief := dates.Format(today)
	if hhmm >= c.String("briefing_at", "05:40") { // kronos.yml: jobs.pulse_rollup.options.briefing_at
		nextBrief = dates.Format(today.AddDate(0, 0, 1))
	}
	for _, m := range send {
		if err := pulseDeliver(c, dir, m, sent, ""); err != nil {
			return err
		}
	}
	for _, m := range deferred {
		sent.Record(m, c.Now, nextBrief)
	}

	// 3. Reports not sent yet (outside quiet hours; outside the cap).
	reports := 0
	if !quiet {
		if reports, err = sendReports(c, dir, g, st, sent, names); err != nil {
			return err
		}
	}

	// 4. Morning brief: yesterday (provisional until final) + today's targets.
	y, haveY := logs.ByDate[dates.Format(today.AddDate(0, 0, -1))]
	if !haveY {
		y.Date = today.AddDate(0, 0, -1)
	}
	brief := pulse.BuildBrief(today, y, haveY, st, g, sent.Deferred(today), names)
	if err := pulseSave(c, pulse.BriefFile(dir), "Pulse part of today's briefing (Kronos)", brief); err != nil {
		return err
	}

	// 5. Housekeeping: 90 days of snapshots and sent log.
	sent.Prune(today, 90)
	if err := pulseSave(c, pulse.SentFile(dir), "Pulse sent messages (Kronos)", sent); err != nil {
		return err
	}
	pruneSnapshots(c, dir, today.AddDate(0, 0, -90))

	if rolled == 0 && len(send)+len(deferred)+reports == 0 {
		c.Log.Skip("No newly final day (brief refreshed)")
		return nil
	}
	c.Log.OK("Rolled %d day(s); %d warning(s) sent, %d deferred; %d report(s)", rolled, len(send), len(deferred), reports)
	return nil
}

// patternWarnings: metrics over their baseline on day d that are on a strike pattern.
func patternWarnings(g pulse.Goals, st *pulse.State, logs pulse.Logs, d time.Time, names map[string]string) []pulse.Message {
	var out []pulse.Message
	for _, m := range g.GoalMetrics() {
		ms := st.Metrics[m.Key]
		overs := pulse.Overs(logs.ByDate, m, ms, d)
		if !overs[len(overs)-1] || !pulse.Pattern(overs) {
			continue
		}
		base := ms.Baseline(d)
		dir := "over"
		if m.Higher {
			dir = "under"
		}
		label := pulse.Label(m, names)
		out = append(out, pulse.Message{
			ID:      "pattern-" + pulse.Slug(label) + "-" + day8(d),
			Type:    pulse.TypePattern,
			Metric:  m.Key,
			Channel: pulse.ChanTelegram,
			Title:   "Pulse",
			Text: fmt.Sprintf("%s has been well %s your usual (%s %s) on %d of the last 7 days.",
				label, dir, pulse.Num(*base), pulse.Unit(m), pulse.OverCount(overs)),
		})
	}
	return out
}

// saveState writes baseline.md only when something changed (then stamps `updated`).
func saveState(c *job.Ctx, dir string, st *pulse.State) error {
	path := pulse.BaselineFile(dir)
	header := "Pulse baselines and goals (Kronos pulse_rollup) — do not edit"
	same, err := pulse.Encode(header, st)
	if err != nil {
		return err
	}
	if old, _ := os.ReadFile(path); string(old) == string(same) {
		return nil
	}
	st.Updated = c.Now.Format("2006-01-02 15:04:05")
	return pulseSave(c, path, header, st)
}

// latestPerMetric keeps one warning per metric (the newest day), and none for a metric
// already warned about in the last 3 days.
func latestPerMetric(ws []pulse.Message, sent *pulse.Sent, today time.Time) []pulse.Message {
	last := map[string]int{}
	for i, w := range ws {
		last[w.Metric] = i
	}
	var out []pulse.Message
	for i, w := range ws {
		if last[w.Metric] == i && !sent.SentSince(pulse.TypePattern, w.Metric, today.AddDate(0, 0, -3)) {
			out = append(out, w)
		}
	}
	return out
}

// weeklyRollup: Sunday is final → week rollup, then weekly goal adjustment.
func weeklyRollup(c *job.Ctx, dir string, g pulse.Goals, st *pulse.State, final []pulse.Day, sunday time.Time, names map[string]string) error {
	week := pulse.ISOWeek(sunday)
	if containsStr(st.RolledWeeks, week) {
		return nil
	}
	monday := sunday.AddDate(0, 0, -6)
	r := pulse.BuildRollup(week, monday, sunday, final, g, st)
	for _, m := range g.GoalMetrics() {
		mr := r.Metrics[m.Key]
		st.Metric(m.Key).AdjustWeek(g, m, week, mr.Median) // goal met when the week's median meets it
	}
	for _, m := range g.GoalMetrics() { // record this week's changes in the rollup
		if gl := st.Metrics[m.Key].Goal; gl != nil {
			for _, h := range gl.History {
				if h.Week == week && h.Change != "started" {
					r.GoalChange = append(r.GoalChange, pulse.GoalChangeRoll{Metric: m.Key, Week: week, Change: h.Change, From: h.From, To: h.To})
				}
			}
		}
	}
	st.RolledWeeks = append(st.RolledWeeks, week)
	return pulseSave(c, pulse.WeeklyFile(dir, week), "Pulse weekly rollup "+week+" (Kronos)", r)
}

// monthlyRollup: the month's last day is final → month rollup.
func monthlyRollup(c *job.Ctx, dir string, g pulse.Goals, st *pulse.State, final []pulse.Day, last time.Time) error {
	month := last.Format("2006-01")
	if containsStr(st.RolledMonths, month) {
		return nil
	}
	first := dates.New(last.Year(), last.Month(), 1)
	r := pulse.BuildRollup(month, first, last, final, g, st)
	st.RolledMonths = append(st.RolledMonths, month)
	return pulseSave(c, pulse.MonthlyFile(dir, last), "Pulse monthly rollup "+month+" (Kronos)", r)
}

// sendReports sends the latest weekly/monthly rollups not yet sent: Telegram (plain numbers
// until Omnimo writes prose) and, for weeks, a short inbox version.
func sendReports(c *job.Ctx, dir string, g pulse.Goals, st *pulse.State, sent *pulse.Sent, names map[string]string) (int, error) {
	n := 0
	for _, week := range lastN(st.RolledWeeks, 1) { // only the latest: no backlog after a first run
		id := "report-weekly-" + week
		r, ok := loadRollup(pulse.WeeklyFile(dir, week))
		if !ok || sent.Has(id) {
			continue
		}
		tg := pulse.Message{ID: id, Type: pulse.TypeReport, Metric: "weekly", Channel: pulse.ChanTelegram,
			Title: "Pulse weekly " + week, Text: pulse.ReportText(r, g, names)}
		if err := pulseDeliver(c, dir, tg, sent, ""); err != nil {
			return n, err
		}
		if short := pulse.ReportShort(r, g, names); short != "" {
			in := pulse.Message{ID: "week-" + strings.ToLower(week), Type: pulse.TypeReport, Metric: "weekly", Channel: pulse.ChanInbox,
				Title: "Your week " + week, Text: short}
			if err := pulseDeliver(c, dir, in, sent, ""); err != nil {
				return n, err
			}
		}
		n++
	}
	for _, month := range lastN(st.RolledMonths, 1) {
		id := "report-monthly-" + month
		t, _ := dates.ParseISO(month + "-01")
		r, ok := loadRollup(pulse.MonthlyFile(dir, t))
		if !ok || sent.Has(id) {
			continue
		}
		m := pulse.Message{ID: id, Type: pulse.TypeReport, Metric: "monthly", Channel: pulse.ChanTelegram,
			Title: "Pulse monthly " + month, Text: pulse.ReportText(r, g, names)}
		if err := pulseDeliver(c, dir, m, sent, ""); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func loadRollup(path string) (pulse.Rollup, bool) {
	var r pulse.Rollup
	raw, err := os.ReadFile(path)
	if err != nil || yaml.Unmarshal(raw, &r) != nil {
		return r, false
	}
	return r, true
}

// pruneSnapshots removes intraday snapshot files older than `before`.
func pruneSnapshots(c *job.Ctx, dir string, before time.Time) {
	files, _ := filepath.Glob(filepath.Join(dir, "state", "intraday-*.yml"))
	cut := "intraday-" + day8(before) + ".yml"
	for _, f := range files {
		if filepath.Base(f) < cut {
			if err := c.Remove(f); err != nil {
				c.Log.Warn("Cannot remove %s: %v", f, err)
			}
		}
	}
}

func lastN(list []string, n int) []string {
	if len(list) > n {
		return list[len(list)-n:]
	}
	return list
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
