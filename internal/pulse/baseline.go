package pulse

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gopkg.in/yaml.v3"

	"gitlab.com/niharokz/kronos/internal/dates"
)

// MinDays is how many logged days Pulse needs before it nudges or sets goals.
const MinDays = 7

// State is baseline.md: rolling baselines, goals and what has been rolled up.
type State struct {
	Updated      string                  `yaml:"updated"`
	DaysWithData int                     `yaml:"days_with_data"` // final days with any metric
	Metrics      map[string]*MetricState `yaml:"metrics"`
	RolledDays   []string                `yaml:"rolled_days"`   // final days already processed
	RolledWeeks  []string                `yaml:"rolled_weeks"`  // e.g. 2026-W43
	RolledMonths []string                `yaml:"rolled_months"` // e.g. 2026-10
}

// MetricState is one metric's baselines and goal.
type MetricState struct {
	Weekday Medians `yaml:"weekday"`
	Weekend Medians `yaml:"weekend"`
	Goal    *Goal   `yaml:"goal,omitempty"`
}

// Medians are the rolling 7- and 28-day medians (nil = not enough data).
type Medians struct {
	M7  *float64 `yaml:"median_7d"`
	M28 *float64 `yaml:"median_28d"`
}

// Goal is a self-tightening goal.
type Goal struct {
	Start   float64      `yaml:"start"`        // the 28-day median when the goal began (clamped)
	Current float64      `yaml:"current"`      // today's goal
	Missed  int          `yaml:"missed_weeks"` // consecutive missed weeks
	History []GoalChange `yaml:"history,omitempty"`
}

// GoalChange is one weekly adjustment.
type GoalChange struct {
	Week   string  `yaml:"week"`
	Change string  `yaml:"change"` // tightened | loosened | started
	From   float64 `yaml:"from"`
	To     float64 `yaml:"to"`
}

// BaselineFile is <rahi>/baseline.md.
func BaselineFile(rahiDir string) string { return filepath.Join(rahiDir, "baseline.md") }

// LoadState reads baseline.md; missing gives an empty state.
func LoadState(path string) (*State, error) {
	s := &State{Metrics: map[string]*MetricState{}}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(raw, s); err != nil {
		return nil, err
	}
	if s.Metrics == nil {
		s.Metrics = map[string]*MetricState{}
	}
	return s, nil
}

// Metric returns (creating) a metric's state.
func (s *State) Metric(key string) *MetricState {
	if s.Metrics[key] == nil {
		s.Metrics[key] = &MetricState{}
	}
	return s.Metrics[key]
}

// Rolled reports whether a day was already processed.
func (s *State) Rolled(day time.Time) bool { return contains(s.RolledDays, dates.Format(day)) }

// Ready is true once enough days exist to nudge and set goals.
func (s *State) Ready() bool { return s.DaysWithData >= MinDays }

// Recompute refreshes all medians from the final days (oldest first) up to and including asOf.
func (s *State) Recompute(days []Day, metrics []Metric, asOf time.Time) {
	n := 0
	for _, d := range days {
		if !d.Date.After(asOf) && (d.ScreenMin != nil || d.Steps != nil || d.SleepMin != nil) {
			n++
		}
	}
	s.DaysWithData = n
	for _, m := range metrics {
		st := s.Metric(m.Key)
		st.Weekday = Medians{M7: windowMedian(days, m, asOf, 7, false), M28: windowMedian(days, m, asOf, 28, false)}
		st.Weekend = Medians{M7: windowMedian(days, m, asOf, 7, true), M28: windowMedian(days, m, asOf, 28, true)}
	}
}

// windowMedian is the median of a metric over the n days ending at asOf, for one day type;
// with fewer than 3 values of that type it falls back to all days in the window.
func windowMedian(days []Day, m Metric, asOf time.Time, n int, weekend bool) *float64 {
	from := asOf.AddDate(0, 0, -n+1)
	var same, all []float64
	for _, d := range days {
		if d.Date.Before(from) || d.Date.After(asOf) {
			continue
		}
		if v := d.Value(m); v != nil {
			all = append(all, *v)
			if Weekend(d.Date) == weekend {
				same = append(same, *v)
			}
		}
	}
	if len(same) >= 3 {
		return Median(same)
	}
	return Median(all)
}

// Baseline is the 28-day median for the day type of `on` (falls back to 7-day).
func (st *MetricState) Baseline(on time.Time) *float64 {
	m := st.Weekday
	if Weekend(on) {
		m = st.Weekend
	}
	if m.M28 != nil {
		return m.M28
	}
	return m.M7
}

// Recent is the 7-day median for the day type of `on`.
func (st *MetricState) Recent(on time.Time) *float64 {
	m := st.Weekday
	if Weekend(on) {
		m = st.Weekend
	}
	if m.M7 != nil {
		return m.M7
	}
	return m.M28
}

// StartGoals begins a goal at the clamped 28-day median for every metric that has none yet.
func (s *State) StartGoals(g Goals, days []Day, asOf time.Time, week string) {
	if !s.Ready() {
		return
	}
	for _, m := range g.GoalMetrics() {
		st := s.Metric(m.Key)
		if st.Goal != nil {
			continue
		}
		med := allMedian(days, m, asOf, 28) // one goal for all days: the plain 28-day median
		if med == nil {
			continue
		}
		v := Round(g.Clamp(m, *med))
		st.Goal = &Goal{Start: v, Current: v, History: []GoalChange{{Week: week, Change: "started", From: v, To: v}}}
	}
}

func allMedian(days []Day, m Metric, asOf time.Time, n int) *float64 {
	from := asOf.AddDate(0, 0, -n+1)
	var vs []float64
	for _, d := range days {
		if !d.Date.Before(from) && !d.Date.After(asOf) {
			if v := d.Value(m); v != nil {
				vs = append(vs, *v)
			}
		}
	}
	return Median(vs)
}

// Over is true when v is well off the baseline: >125% (lower is better) or <75% (higher is better).
func Over(m Metric, v, base float64) bool {
	if base <= 0 {
		return false
	}
	if m.Higher {
		return v < 0.75*base
	}
	return v > 1.25*base
}

// Pattern is true for 2 of the last 3 or 3 of the last 7 days over (overs: oldest first).
func Pattern(overs []bool) bool {
	count := func(n int) int {
		c := 0
		for i := len(overs) - n; i < len(overs); i++ {
			if i >= 0 && overs[i] {
				c++
			}
		}
		return c
	}
	return count(3) >= 2 || count(7) >= 3
}

// Meets reports whether v satisfies the goal.
func Meets(m Metric, v, goal float64) bool {
	if m.Higher {
		return v >= goal
	}
	return v <= goal
}

// AdjustWeek applies one week's result: met tightens 5%, two missed weeks in a row loosen 5%
// (never past the start), always within the hard limits. weekMedian nil = no data, no change.
func (st *MetricState) AdjustWeek(g Goals, m Metric, week string, weekMedian *float64) {
	gl := st.Goal
	if gl == nil || weekMedian == nil {
		return
	}
	if n := len(gl.History); n > 0 && gl.History[n-1].Week == week && gl.History[n-1].Change == "started" {
		return // a goal started this week is judged from next week on
	}
	from := gl.Current
	change := ""
	if Meets(m, *weekMedian, gl.Current) {
		gl.Missed = 0
		if m.Higher {
			gl.Current *= 1.05
		} else {
			gl.Current *= 0.95
		}
		change = "tightened"
	} else if gl.Missed++; gl.Missed >= 2 {
		gl.Missed = 0
		if m.Higher {
			gl.Current = math.Max(gl.Current*0.95, gl.Start)
		} else {
			gl.Current = math.Min(gl.Current*1.05, gl.Start)
		}
		change = "loosened"
	}
	gl.Current = Round(g.Clamp(m, gl.Current))
	if change != "" && gl.Current != from {
		gl.History = append(gl.History, GoalChange{Week: week, Change: change, From: from, To: gl.Current})
	}
}

// Target is today's target: after a day worse than baseline, yesterday ∓10%,
// never stricter than the goal; otherwise the goal. ok=false without a goal.
func (st *MetricState) Target(g Goals, m Metric, yesterday *float64, yDate time.Time) (float64, bool) {
	if st.Goal == nil {
		return 0, false
	}
	t := st.Goal.Current
	if base := st.Baseline(yDate); yesterday != nil && base != nil && worse(m, *yesterday, *base) {
		if m.Higher {
			t = math.Min(*yesterday*1.1, st.Goal.Current)
		} else {
			t = math.Max(*yesterday*0.9, st.Goal.Current)
		}
	}
	return Round(g.Clamp(m, t)), true
}

func worse(m Metric, v, base float64) bool {
	if m.Higher {
		return v < base
	}
	return v > base
}

// Median of vs; nil when empty.
func Median(vs []float64) *float64 {
	if len(vs) == 0 {
		return nil
	}
	s := append([]float64(nil), vs...)
	sort.Float64s(s)
	mid := len(s) / 2
	v := s[mid]
	if len(s)%2 == 0 {
		v = (s[mid-1] + s[mid]) / 2
	}
	v = Round(v)
	return &v
}

// Round keeps one decimal.
func Round(v float64) float64 { return math.Round(v*10) / 10 }

// ISOWeek is "2026-W43".
func ISOWeek(d time.Time) string {
	y, w := d.ISOWeek()
	return fmt.Sprintf("%d-W%02d", y, w)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Overs lists, oldest first, whether each of the 7 days ending at `end` was over its baseline.
// Missing days count as not over.
func Overs(byDate map[string]Day, m Metric, st *MetricState, end time.Time) []bool {
	out := make([]bool, 7)
	if st == nil {
		return out
	}
	for i := 0; i < 7; i++ {
		day := end.AddDate(0, 0, i-6)
		d, ok := byDate[dates.Format(day)]
		if !ok || !d.Final {
			continue
		}
		if v, base := d.Value(m), st.Baseline(day); v != nil && base != nil {
			out[i] = Over(m, *v, *base)
		}
	}
	return out
}

// OverCount is how many of the overs are true.
func OverCount(overs []bool) int {
	n := 0
	for _, o := range overs {
		if o {
			n++
		}
	}
	return n
}
