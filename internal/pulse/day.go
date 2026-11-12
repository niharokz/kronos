package pulse

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"gitlab.com/niharokz/kronos/internal/dates"
)

// Day is one phone daily log (<daily_logs>/YYYYMMDD.md, Kshan; RahiOS schema).
// nil numbers mean missing, never 0; mindful/modes/created may be absent (Kshan) = missing.
type Day struct {
	Date       time.Time
	Final      bool
	Steps      *float64
	SleepMin   *float64
	ScreenMin  *float64
	Unlocks    *float64
	FirstUse   string             // "HH:MM" or ""
	LastUse    string             // "HH:MM" or ""
	Apps       map[string]App     // by package; nil when screen data is missing
	TurnedBack map[string]float64 // mindful.by_app[pkg].turned_back
}

// App is one apps: entry.
type App struct {
	Name  string
	Min   float64
	Opens float64
}

// DayFile is the log path for a date.
func DayFile(dailyDir string, d time.Time) string {
	return filepath.Join(dailyDir, d.Format("20060102")+".md")
}

// LoadDay reads one daily log; a missing file is (Day{}, false, nil).
func LoadDay(path string) (Day, bool, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Day{}, false, nil
	}
	if err != nil {
		return Day{}, false, err
	}
	d, err := ParseDay(raw)
	if err != nil {
		return Day{}, false, fmt.Errorf("%s: cannot parse (the phone may be mid-write; retried next run): %v", filepath.Base(path), err)
	}
	if d.Date.IsZero() { // fall back to the file name
		if t, ok := dates.ParseISO(strings.TrimSuffix(filepath.Base(path), ".md")); ok {
			d.Date = t
		}
	}
	return d, true, nil
}

// ParseDay parses a daily log's YAML.
func ParseDay(raw []byte) (Day, error) {
	var m map[string]any
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return Day{}, err
	}
	if m == nil {
		return Day{}, fmt.Errorf("empty file")
	}
	var d Day
	if t, ok := dates.ParseISO(str(m["date"])); ok {
		d.Date = t
	}
	d.Final, _ = m["final"].(bool)

	if h, ok := m["health"].(map[string]any); ok && !truthy(m["health_error"]) { // health_error days count as missing
		d.Steps = num(h["steps"])
		if s, ok := h["sleep"].(map[string]any); ok {
			d.SleepMin = num(s["min"])
		}
	}
	if s, ok := m["screen"].(map[string]any); ok {
		d.ScreenMin = num(s["total_min"])
		d.Unlocks = num(s["unlocks"])
		d.FirstUse, d.LastUse = str(s["first_use"]), str(s["last_use"])
		d.Apps = map[string]App{}
		if list, ok := m["apps"].([]any); ok {
			for _, it := range list {
				a, ok := it.(map[string]any)
				if !ok || str(a["pkg"]) == "" {
					continue
				}
				d.Apps[str(a["pkg"])] = App{Name: str(a["name"]), Min: val(a["min"]), Opens: val(a["opens"])}
			}
		}
	}
	if mf, ok := m["mindful"].(map[string]any); ok {
		if by, ok := mf["by_app"].(map[string]any); ok {
			d.TurnedBack = map[string]float64{}
			for pkg, v := range by {
				if a, ok := v.(map[string]any); ok {
					d.TurnedBack[pkg] = val(a["turned_back"])
				}
			}
		}
	}
	return d, nil
}

// Value is the day's value for a metric; nil when missing.
func (d Day) Value(m Metric) *float64 {
	switch {
	case m.Key == KeyScreen:
		return d.ScreenMin
	case m.Key == KeySteps:
		return d.Steps
	case m.Key == KeySleep:
		return d.SleepMin
	case m.Key == KeyUnlocks:
		return d.Unlocks
	case m.App != "":
		if d.Apps == nil {
			return nil // no screen data that day
		}
		v := d.Apps[m.App].Min // a tracked app missing from the list counts as 0
		return &v
	}
	return nil
}

// AppName is the app's display name from the log, else its package.
func (d Day) AppName(pkg string) string {
	if a, ok := d.Apps[pkg]; ok && a.Name != "" {
		return a.Name
	}
	return pkg
}

// LogDates lists the dates that have a daily log in any of the folders, oldest first.
func LogDates(dailyDirs ...string) ([]time.Time, error) {
	seen := map[string]bool{}
	var out []time.Time
	for _, dir := range dailyDirs {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			name := strings.TrimSuffix(e.Name(), ".md")
			if e.IsDir() || len(name) != 8 || !strings.HasSuffix(e.Name(), ".md") || seen[name] {
				continue
			}
			if t, ok := dates.ParseISO(name); ok {
				seen[name] = true
				out = append(out, t)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out, nil
}

// Weekend is Saturday or Sunday.
func Weekend(d time.Time) bool { return d.Weekday() == time.Saturday || d.Weekday() == time.Sunday }

func num(v any) *float64 {
	switch x := v.(type) {
	case int:
		f := float64(x)
		return &f
	case float64:
		return &x
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
			return &f
		}
	}
	return nil
}

func val(v any) float64 {
	if p := num(v); p != nil {
		return *p
	}
	return 0
}

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(x)
	case time.Time:
		return dates.Format(x)
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return strings.TrimSpace(x) != ""
	}
	return true
}
