package pulse

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gitlab.com/niharokz/kronos/internal/dates"
)

// Rollup is weekly/YYYY-Www.md or monthly/YYYYMM.md.
type Rollup struct {
	Period     string                `yaml:"period"` // 2026-W43 | 2026-10
	From       string                `yaml:"from"`
	To         string                `yaml:"to"`
	DaysLogged int                   `yaml:"days_logged"`
	Metrics    map[string]MetricRoll `yaml:"metrics"`
	Apps       []AppRoll             `yaml:"apps,omitempty"` // every app seen, most minutes first
	Strikes    map[string]int        `yaml:"strikes,omitempty"`
	GoalChange []GoalChangeRoll      `yaml:"goal_changes,omitempty"`
}

// MetricRoll summarises one metric over the period.
type MetricRoll struct {
	Days     int      `yaml:"days"`
	Average  *float64 `yaml:"average"`
	Median   *float64 `yaml:"median"`
	Best     *DayVal  `yaml:"best,omitempty"`
	Worst    *DayVal  `yaml:"worst,omitempty"`
	Goal     *float64 `yaml:"goal,omitempty"`
	GoalDays *int     `yaml:"goal_days,omitempty"` // days the goal was met
}

// DayVal is a value on a date.
type DayVal struct {
	Date  string  `yaml:"date"`
	Value float64 `yaml:"value"`
}

// AppRoll is one app's total over the period.
type AppRoll struct {
	Pkg      string  `yaml:"pkg"`
	Name     string  `yaml:"name"`
	TotalMin float64 `yaml:"total_min"`
	Days     int     `yaml:"days"`
}

// GoalChangeRoll is a goal change within the period.
type GoalChangeRoll struct {
	Metric string  `yaml:"metric"`
	Week   string  `yaml:"week"`
	Change string  `yaml:"change"`
	From   float64 `yaml:"from"`
	To     float64 `yaml:"to"`
}

// WeeklyFile and MonthlyFile are the rollup paths.
func WeeklyFile(rahiDir, week string) string {
	return filepath.Join(rahiDir, "weekly", week+".md")
}
func MonthlyFile(rahiDir string, d time.Time) string {
	return filepath.Join(rahiDir, "monthly", d.Format("200601")+".md")
}

// BuildRollup summarises the final days in [from, to].
func BuildRollup(period string, from, to time.Time, days []Day, g Goals, st *State) Rollup {
	r := Rollup{Period: period, From: dates.Format(from), To: dates.Format(to), Metrics: map[string]MetricRoll{}, Strikes: map[string]int{}}
	var in []Day
	for _, d := range days {
		if !d.Date.Before(from) && !d.Date.After(to) {
			in = append(in, d)
		}
	}
	r.DaysLogged = len(in)
	metrics := append(g.GoalMetrics(), Metric{Key: KeyUnlocks})
	for _, m := range metrics {
		mr := MetricRoll{}
		var vs []float64
		sum := 0.0
		ms := st.Metrics[m.Key]
		for _, d := range in {
			v := d.Value(m)
			if v == nil {
				continue
			}
			vs = append(vs, *v)
			sum += *v
			dv := &DayVal{Date: dates.Format(d.Date), Value: *v}
			if mr.Best == nil || better(m, *v, mr.Best.Value) {
				mr.Best = dv
			}
			if mr.Worst == nil || better(m, mr.Worst.Value, *v) {
				mr.Worst = dv
			}
			if ms != nil {
				if base := ms.Baseline(d.Date); base != nil && Over(m, *v, *base) {
					r.Strikes[m.Key]++
				}
			}
		}
		mr.Days = len(vs)
		if len(vs) > 0 {
			avg := Round(sum / float64(len(vs)))
			mr.Average, mr.Median = &avg, Median(vs)
		}
		if ms != nil && ms.Goal != nil && m.Key != KeyUnlocks {
			goal, n := ms.Goal.Current, 0
			for _, v := range vs {
				if Meets(m, v, goal) {
					n++
				}
			}
			mr.Goal, mr.GoalDays = &goal, &n
			for _, h := range ms.Goal.History {
				if inPeriod(h.Week, from, to) && h.Change != "started" {
					r.GoalChange = append(r.GoalChange, GoalChangeRoll{Metric: m.Key, Week: h.Week, Change: h.Change, From: h.From, To: h.To})
				}
			}
		}
		if m.Key == KeyUnlocks { // no goal or strikes for unlocks
			mr.Best, mr.Worst = nil, nil
		}
		r.Metrics[m.Key] = mr
	}
	apps := map[string]*AppRoll{}
	for _, d := range in {
		for pkg, a := range d.Apps {
			ar := apps[pkg]
			if ar == nil {
				ar = &AppRoll{Pkg: pkg, Name: a.Name}
				apps[pkg] = ar
			}
			ar.TotalMin = Round(ar.TotalMin + a.Min)
			ar.Days++
		}
	}
	for _, a := range apps {
		r.Apps = append(r.Apps, *a)
	}
	sort.Slice(r.Apps, func(i, j int) bool {
		if r.Apps[i].TotalMin != r.Apps[j].TotalMin {
			return r.Apps[i].TotalMin > r.Apps[j].TotalMin
		}
		return r.Apps[i].Pkg < r.Apps[j].Pkg
	})
	for k, v := range r.Strikes {
		if v == 0 {
			delete(r.Strikes, k)
		}
	}
	return r
}

// better: a beats b for this metric.
func better(m Metric, a, b float64) bool {
	if m.Higher {
		return a > b
	}
	return a < b
}

// inPeriod: does an ISO week label fall within [from, to]?
func inPeriod(week string, from, to time.Time) bool {
	return week >= ISOWeek(from) && week <= ISOWeek(to)
}

// ReportText is the plain-numbers report (Telegram until Omnimo can write prose).
func ReportText(r Rollup, g Goals, names map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s · %s → %s · %d days logged\n", r.Period, r.From, r.To, r.DaysLogged)
	for _, m := range append(g.GoalMetrics(), Metric{Key: KeyUnlocks}) {
		mr, ok := r.Metrics[m.Key]
		if !ok || mr.Median == nil {
			continue
		}
		line := fmt.Sprintf("%s: median %s %s, avg %s", Label(m, names), Num(*mr.Median), Unit(m), Num(*mr.Average))
		if mr.Goal != nil {
			line += fmt.Sprintf(" · goal %s (met %d/%d days)", Num(*mr.Goal), *mr.GoalDays, mr.Days)
		}
		if n := r.Strikes[m.Key]; n > 0 {
			line += fmt.Sprintf(" · %d strike day(s)", n)
		}
		b.WriteString(line + "\n")
	}
	for i, a := range r.Apps {
		if i == 5 {
			break
		}
		if i == 0 {
			b.WriteString("Top apps: ")
		} else {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s %s min", a.Name, Num(a.TotalMin))
	}
	if len(r.Apps) > 0 {
		b.WriteString("\n")
	}
	for _, c := range r.GoalChange {
		fmt.Fprintf(&b, "Goal %s: %s → %s (%s)\n", c.Metric, Num(c.From), Num(c.To), c.Change)
	}
	return strings.TrimSpace(b.String())
}

// ReportShort is the Monday inbox version: one line per goal.
func ReportShort(r Rollup, g Goals, names map[string]string) string {
	var lines []string
	for _, m := range g.GoalMetrics() {
		mr, ok := r.Metrics[m.Key]
		if !ok || mr.Median == nil || mr.Goal == nil {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s: %s %s (goal %s, met %d/%d)", Label(m, names), Num(*mr.Median), Unit(m), Num(*mr.Goal), *mr.GoalDays, mr.Days))
	}
	return strings.Join(lines, "\n")
}

// Label is a metric's display name.
func Label(m Metric, names map[string]string) string {
	switch m.Key {
	case KeyScreen:
		return "Screen time"
	case KeySteps:
		return "Steps"
	case KeySleep:
		return "Sleep"
	case KeyUnlocks:
		return "Unlocks"
	}
	if n := names[m.App]; n != "" {
		return n
	}
	return m.App
}

// Unit is a metric's unit.
func Unit(m Metric) string {
	switch m.Key {
	case KeySteps:
		return "steps"
	case KeyUnlocks:
		return "unlocks"
	}
	return "min"
}
