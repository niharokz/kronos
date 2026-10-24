package pulse

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Metric keys.
const (
	KeyScreen  = "screen"  // screen.total_min — lower is better
	KeySteps   = "steps"   // health.steps — higher is better
	KeySleep   = "sleep"   // health.sleep.min — higher is better
	KeyUnlocks = "unlocks" // recorded in rollups only, no goal
)

// Metric is one thing Pulse tracks; App is set for "app:<pkg>".
type Metric struct {
	Key    string
	Higher bool   // true: more is better (steps, sleep)
	App    string // package, for per-app metrics
}

// AppMetric is the metric for one tracked app.
func AppMetric(pkg string) Metric { return Metric{Key: "app:" + pkg, App: pkg} }

// Goals is goals.md: your settings. Kronos only reads it (and writes a default if missing).
type Goals struct {
	Limits      map[string]Limit `yaml:"limits"`
	TrackedApps []string         `yaml:"tracked_apps"`
	QuietHours  Window           `yaml:"quiet_hours"`
	MaxMessages int              `yaml:"max_messages_per_day"`
	StepNudges  StepNudges       `yaml:"step_nudges"`
}

// Limit is a hard bound a goal is always clamped to.
type Limit struct {
	Min *float64 `yaml:"min,omitempty"`
	Max *float64 `yaml:"max,omitempty"`
}

// Window is a daily time range "HH:MM"–"HH:MM"; it may wrap midnight.
type Window struct {
	Start string `yaml:"start"`
	End   string `yaml:"end"`
}

// StepNudges are the earliest times for the soft and the final step nudge.
type StepNudges struct {
	Soft  string `yaml:"soft"`
	Final string `yaml:"final"`
}

// DefaultGoals is written to goals.md when it doesn't exist.
const DefaultGoals = `# Pulse goals — read by Kronos (pulse_watch, pulse_rollup), never written after creation.
# Edit freely; changes apply on the next run.

# Hard limits: goals tighten/loosen automatically but always stay inside these.
limits:
  screen: { min: 120 }      # screen-time goal never below 120 min
  steps: { max: 12000 }     # step goal never above 12000
  sleep: { max: 480 }       # sleep goal never above 8 h
  app: { min: 10 }          # default floor for every tracked app's goal (minutes)
  # app:com.instagram.android: { min: 15 }   # per-app override

# Apps with their own goal and nudges (package names, see apps: in a daily log).
tracked_apps: []
  # - com.instagram.android
  # - com.google.android.youtube

# No nudges in this window (pattern warnings move to the morning briefing).
quiet_hours: { start: '22:30', end: '08:00' }

# Nudges + warnings per day (weekly/monthly reports don't count).
max_messages_per_day: 3

# Earliest times for the soft and the final "unlikely to reach steps" nudge.
step_nudges: { soft: '16:00', final: '20:30' }
`

// LoadGoals reads goals.md; missing is (defaults, false, nil).
func LoadGoals(path string) (Goals, bool, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		g, _ := ParseGoals([]byte(DefaultGoals))
		return g, false, nil
	}
	if err != nil {
		return Goals{}, false, err
	}
	g, err := ParseGoals(raw)
	if err != nil {
		return Goals{}, true, err
	}
	return g, true, nil
}

// ParseGoals parses goals.md and fills defaults.
func ParseGoals(raw []byte) (Goals, error) {
	var g Goals
	if err := yaml.Unmarshal(raw, &g); err != nil {
		return Goals{}, err
	}
	if g.Limits == nil {
		g.Limits = map[string]Limit{}
	}
	if g.QuietHours.Start == "" || g.QuietHours.End == "" {
		g.QuietHours = Window{Start: "22:30", End: "08:00"}
	}
	if g.MaxMessages <= 0 {
		g.MaxMessages = 3
	}
	if g.StepNudges.Soft == "" {
		g.StepNudges.Soft = "16:00"
	}
	if g.StepNudges.Final == "" {
		g.StepNudges.Final = "20:30"
	}
	return g, nil
}

// GoalMetrics are the metrics that get goals: screen, each tracked app, steps, sleep.
func (g Goals) GoalMetrics() []Metric {
	ms := []Metric{{Key: KeyScreen}}
	for _, pkg := range g.TrackedApps {
		if pkg = strings.TrimSpace(pkg); pkg != "" {
			ms = append(ms, AppMetric(pkg))
		}
	}
	return append(ms, Metric{Key: KeySteps, Higher: true}, Metric{Key: KeySleep, Higher: true})
}

// Clamp keeps v inside the metric's hard limits.
func (g Goals) Clamp(m Metric, v float64) float64 {
	l, ok := g.Limits[m.Key]
	if !ok && m.App != "" {
		l = g.Limits["app"]
	}
	if l.Min != nil && v < *l.Min {
		v = *l.Min
	}
	if l.Max != nil && v > *l.Max {
		v = *l.Max
	}
	return v
}

// Quiet reports whether "HH:MM" falls in the quiet window.
func (g Goals) Quiet(hhmm string) bool { return g.QuietHours.Contains(hhmm) }

// Contains reports whether hhmm is inside the window (start inclusive, end exclusive).
func (w Window) Contains(hhmm string) bool {
	if w.Start <= w.End {
		return hhmm >= w.Start && hhmm < w.End
	}
	return hhmm >= w.Start || hhmm < w.End // wraps midnight
}

// GoalsFile is <rahi>/goals.md.
func GoalsFile(rahiDir string) string { return filepath.Join(rahiDir, "goals.md") }
