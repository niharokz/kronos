package pulse

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"gitlab.com/niharokz/kronos/internal/dates"
)

// Brief is state/brief.yml: the Pulse part of today's morning briefing.
type Brief struct {
	Date  string   `yaml:"date"`  // briefing day
	Lines []string `yaml:"lines"` // verdict, targets, deferred warnings
}

// BriefFile is <rahi>/state/brief.yml.
func BriefFile(rahiDir string) string { return filepath.Join(rahiDir, "state", "brief.yml") }

// LoadBrief reads brief.yml; ok=false when missing or not for `day`.
func LoadBrief(path string, day time.Time) (Brief, bool) {
	var b Brief
	raw, err := os.ReadFile(path)
	if err != nil || yaml.Unmarshal(raw, &b) != nil || b.Date != dates.Format(day) {
		return Brief{}, false
	}
	return b, len(b.Lines) > 0
}

// BuildBrief writes yesterday's verdict and today's targets. yesterday may be provisional
// (not final yet); final days are only used for baselines.
func BuildBrief(today time.Time, y Day, haveY bool, st *State, g Goals, deferred []SentItem, names map[string]string) Brief {
	b := Brief{Date: dates.Format(today)}
	if !st.Ready() {
		b.Lines = append(b.Lines, fmt.Sprintf("Pulse is learning: %d of %d days logged, nudges start after that.", st.DaysWithData, MinDays))
		return b
	}
	if haveY {
		tag := ""
		if !y.Final {
			tag = " (provisional)"
		}
		var parts, overs []string
		for _, m := range g.GoalMetrics() {
			v, ms := y.Value(m), st.Metrics[m.Key]
			if v == nil || ms == nil || ms.Goal == nil {
				continue
			}
			mark := "✓"
			if !Meets(m, *v, ms.Goal.Current) {
				mark = "✗"
			}
			parts = append(parts, fmt.Sprintf("%s %s %s", Label(m, names), Num(*v), mark))
			if base := ms.Baseline(y.Date); base != nil && Over(m, *v, *base) {
				overs = append(overs, Label(m, names))
			}
		}
		if len(parts) > 0 {
			b.Lines = append(b.Lines, "Yesterday"+tag+": "+strings.Join(parts, " · "))
		}
		if len(overs) > 0 {
			b.Lines = append(b.Lines, "Well off your usual: "+strings.Join(overs, ", "))
		}
	}
	var targets []string
	for _, m := range g.GoalMetrics() {
		ms := st.Metrics[m.Key]
		if ms == nil {
			continue
		}
		var yv *float64
		if haveY {
			yv = y.Value(m)
		}
		if t, ok := ms.Target(g, m, yv, y.Date); ok {
			sign := "≤"
			if m.Higher {
				sign = "≥"
			}
			targets = append(targets, fmt.Sprintf("%s %s %s %s", Label(m, names), sign, Num(t), Unit(m)))
		}
	}
	if len(targets) > 0 {
		b.Lines = append(b.Lines, "Today: "+strings.Join(targets, " · "))
	}
	for _, d := range deferred {
		b.Lines = append(b.Lines, "⚠ "+d.Text)
	}
	return b
}

// Text is the block nimo_briefing appends to the agenda.
func (b Brief) Text() string {
	return "# Pulse\n" + strings.Join(b.Lines, "\n") + "\n"
}
