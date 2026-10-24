// Package fixture writes fake Rahi daily logs (and intraday step snapshots) into a
// sandbox folder, for testing the pulse_* jobs with `kronos run --rahi-path`.
package fixture

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gitlab.com/niharokz/kronos/internal/dates"
	"gitlab.com/niharokz/kronos/internal/pulse"
	"gitlab.com/niharokz/kronos/internal/store"
)

// Marker marks a folder as a fixture sandbox, so --force only ever wipes fixtures.
const Marker = ".pulse-fixture"

// Package names used by the fake logs.
const (
	Instagram = "com.instagram.android"
	YouTube   = "com.google.android.youtube"
	WhatsApp  = "com.whatsapp"
	Chrome    = "com.android.chrome"
)

// Options describe the fake history. Day offsets count back from Today (1 = yesterday).
type Options struct {
	Out         string
	Today       time.Time // the day you'll pass to --now; logs end the day before
	Days        int       // final days before Today (default 35)
	TodayAt     string    // "HH:MM": also write a partial (non-final) log for Today
	TodaySteps  int       // steps so far in that partial log (0 = a normal day's pace)
	Heavy       int       // one-off heavy day N days ago (0 = none)
	StreakApp   string    // app on a streak ending yesterday (and carrying on today)
	StreakDays  int       // streak length
	NullHealth  []int     // days with health: null
	HealthError []int     // days with health_error set (health counts as missing)
	MissingApp  string    // tracked app absent from apps: (counts as 0)
	MissingDays int       // for the last N days
	Tracked     []string  // goals.md tracked_apps (default Instagram, YouTube)
	Seed        int64
	Force       bool // replace an earlier fixture in Out
}

// Rahi daily log, marshalled in Rahi's field order. nil pointers become null.
type logFile struct {
	Date            string   `yaml:"date"`
	Updated         string   `yaml:"updated"`
	Final           bool     `yaml:"final"`
	FinalAt         string   `yaml:"final_at,omitempty"`
	HealthUpdated   string   `yaml:"health_updated,omitempty"`
	HealthError     string   `yaml:"health_error,omitempty"`
	HealthSourceApp string   `yaml:"health_source_app"`
	Health          *health  `yaml:"health"`
	Screen          screen   `yaml:"screen"`
	Apps            []app    `yaml:"apps"`
	Mindful         mindful  `yaml:"mindful"`
	Modes           []string `yaml:"modes"`
}

type health struct {
	Steps      int     `yaml:"steps"`
	DistanceKm float64 `yaml:"distance_km"`
	ActiveKcal int     `yaml:"active_kcal"`
	ActiveMin  int     `yaml:"active_min"`
	Sleep      sleep   `yaml:"sleep"`
}

type sleep struct {
	Start string `yaml:"start"`
	End   string `yaml:"end"`
	Min   int    `yaml:"min"`
}

type screen struct {
	TotalMin int    `yaml:"total_min"`
	Unlocks  int    `yaml:"unlocks"`
	FirstUse string `yaml:"first_use"`
	LastUse  string `yaml:"last_use"`
}

type app struct {
	Pkg   string `yaml:"pkg"`
	Name  string `yaml:"name"`
	Min   int    `yaml:"min"`
	Opens int    `yaml:"opens"`
}

type mindful struct {
	Opens      int                   `yaml:"opens"`
	WaitedS    int                   `yaml:"waited_s"`
	TurnedBack int                   `yaml:"turned_back"`
	ByApp      map[string]mindfulApp `yaml:"by_app"`
}

type mindfulApp struct {
	Opens      int `yaml:"opens"`
	TurnedBack int `yaml:"turned_back"`
}

// Typical minutes per app (weekday); weekends get 30% more.
var usual = []struct {
	pkg, name string
	min       float64
}{
	{Instagram, "Instagram", 30},
	{YouTube, "YouTube", 25},
	{WhatsApp, "WhatsApp", 20},
	{Chrome, "Chrome", 15},
}

// Write builds the sandbox and returns one summary line per pattern.
func Write(o Options) ([]string, error) {
	if o.Out == "" {
		return nil, fmt.Errorf("--out is required")
	}
	if o.Days <= 0 {
		o.Days = 35
	}
	if o.Today.IsZero() {
		o.Today = dates.Today(time.Now())
	}
	if len(o.Tracked) == 0 {
		o.Tracked = []string{Instagram, YouTube}
	}
	if err := prepare(o.Out, o.Force); err != nil {
		return nil, err
	}
	r := rand.New(rand.NewSource(o.Seed))

	for i := o.Days; i >= 1; i-- {
		d := o.Today.AddDate(0, 0, -i)
		lf, steps := day(r, o, d, i, 1)
		if err := save(pulse.DayFile(o.Out, d), "Rahi daily log (fake — kronos pulse-fixture)", lf); err != nil {
			return nil, err
		}
		if steps > 0 { // the step forecast learns from these
			if err := save(pulse.IntradayFile(o.Out, d), "Pulse intraday snapshots (fake — kronos pulse-fixture)", snapshots(d, lf, steps, o.Tracked)); err != nil {
				return nil, err
			}
		}
	}
	if o.TodayAt != "" {
		lf, _ := day(r, o, o.Today, 0, dayShare(o.TodayAt))
		lf.Final, lf.FinalAt, lf.Updated = false, "", dates.Format(o.Today)+"T"+o.TodayAt
		if lf.Health != nil {
			lf.HealthUpdated = lf.Updated
			if o.TodaySteps > 0 {
				lf.Health.Steps = o.TodaySteps
			}
		}
		if err := save(pulse.DayFile(o.Out, o.Today), "Rahi daily log (fake — kronos pulse-fixture)", lf); err != nil {
			return nil, err
		}
	}
	goals := strings.Replace(pulse.DefaultGoals, "tracked_apps: []", "tracked_apps: ["+strings.Join(o.Tracked, ", ")+"]", 1)
	if err := store.AtomicWrite(pulse.GoalsFile(o.Out), []byte(goals)); err != nil {
		return nil, err
	}
	return summary(o), nil
}

// prepare makes Out, refusing a folder that isn't an earlier fixture (it could be your vault).
func prepare(out string, force bool) error {
	entries, err := os.ReadDir(out)
	switch {
	case os.IsNotExist(err) || (err == nil && len(entries) == 0):
	case err != nil:
		return err
	case !force:
		return fmt.Errorf("%s is not empty (use --force to replace an earlier fixture)", out)
	default:
		if _, err := os.Stat(filepath.Join(out, Marker)); err != nil {
			return fmt.Errorf("%s has no %s marker — not a fixture folder, refusing to wipe it", out, Marker)
		}
		if err := os.RemoveAll(out); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Join(out, "daily"), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, Marker), []byte("written by kronos pulse-fixture; safe to delete\n"), 0o644)
}

// day makes one log; share < 1 scales a partial day. ago is days before Today (0 = Today).
func day(r *rand.Rand, o Options, d time.Time, ago int, share float64) (logFile, int) {
	weekend := pulse.Weekend(d)
	jit := func(v, sd float64) float64 { return math.Max(0, v+r.NormFloat64()*sd) }
	heavy := ago > 0 && ago == o.Heavy
	streak := o.StreakApp != "" && ago <= o.StreakDays // carries on today
	missing := o.MissingApp != "" && ago <= o.MissingDays

	var apps []app
	total := jit(45, 10) // everything else
	for _, u := range usual {
		v := u.min
		if weekend {
			v *= 1.3
		}
		v = jit(v, v*0.25)
		if heavy && (u.pkg == Instagram || u.pkg == YouTube) {
			v *= 3.5
		}
		if streak && u.pkg == o.StreakApp {
			v = jit(u.min*3.2, 8)
		}
		if missing && u.pkg == o.MissingApp {
			continue
		}
		total += v
		apps = append(apps, app{Pkg: u.pkg, Name: u.name, Min: round(v * share), Opens: round(v/4*share) + 1})
	}
	if streak && !hasPkg(o.StreakApp) && !missing { // a streak on an app the fixture doesn't know
		v := jit(80, 10)
		total += v
		apps = append(apps, app{Pkg: o.StreakApp, Name: shortName(o.StreakApp), Min: round(v * share), Opens: round(v/4*share) + 1})
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].Min > apps[j].Min })

	steps := jit(8000, 1200)
	if weekend {
		steps = jit(6000, 1500)
	}
	sleepMin := jit(420, 30)
	if heavy {
		steps, sleepMin = jit(2500, 300), jit(300, 20)
	}
	lastUse := "23:" + fmt.Sprintf("%02d", r.Intn(50))
	if heavy {
		lastUse = "01:40"
	}
	lf := logFile{
		Date:            dates.Format(d),
		Updated:         dates.Format(d) + "T" + lastUse,
		Final:           true,
		FinalAt:         dates.Format(d.AddDate(0, 0, 1)) + "T00:10",
		HealthUpdated:   dates.Format(d) + "T" + lastUse,
		HealthSourceApp: "com.google.android.apps.fitness",
		Health: &health{
			Steps:      round(steps * share),
			DistanceKm: math.Round(steps*share*0.00075*100) / 100,
			ActiveKcal: round(steps * share * 0.04),
			ActiveMin:  round(steps * share / 110),
			Sleep:      sleep{Start: "23:30", End: clock(23*60 + 30 + int(sleepMin)), Min: round(sleepMin)},
		},
		Screen:  screen{TotalMin: round(total * share), Unlocks: round(jit(60, 10) * share), FirstUse: "07:" + fmt.Sprintf("%02d", 5+r.Intn(40)), LastUse: lastUse},
		Apps:    apps,
		Mindful: mindful{ByApp: map[string]mindfulApp{}},
		Modes:   []string{},
	}
	if share < 1 {
		lf.Screen.LastUse = ""
	}
	switch {
	case containsInt(o.NullHealth, ago):
		lf.Health, lf.HealthUpdated = nil, ""
	case containsInt(o.HealthError, ago):
		lf.HealthError = "permission revoked" // health kept (stale) — Pulse must ignore it
	}
	if lf.Health == nil || lf.HealthError != "" {
		return lf, 0
	}
	return lf, lf.Health.Steps
}

// snapshots fakes every-30-minute snapshots (08:00–22:30) on a morning/lunch/evening curve.
func snapshots(d time.Time, lf logFile, steps int, tracked []string) pulse.Intraday {
	in := pulse.Intraday{Date: dates.Format(d)}
	for m := 8 * 60; m <= 22*60+30; m += 30 {
		at := clock(m)
		s := dayShare(at)
		st, sc := float64(round(float64(steps)*curve(m))), float64(round(float64(lf.Screen.TotalMin)*s))
		snap := pulse.Snapshot{At: at, Steps: &st, ScreenMin: &sc, Apps: map[string]float64{}}
		for _, a := range lf.Apps {
			if containsStr(tracked, a.Pkg) {
				snap.Apps[a.Pkg] = float64(round(float64(a.Min) * s))
			}
		}
		in.Snapshots = append(in.Snapshots, snap)
	}
	return in
}

// curve is the share of the day's steps done by minute m: bursts at 08–09, 12–13 and 18–19:30.
func curve(m int) float64 {
	type burst struct{ from, to int }
	bursts := []burst{{8 * 60, 9 * 60}, {12 * 60, 13 * 60}, {18 * 60, 19*60 + 30}}
	const base = 0.4 // spread evenly over 07:00–23:00; the rest in bursts
	done := base * clamp(float64(m-7*60)/(16*60))
	for _, b := range bursts {
		done += (1 - base) / float64(len(bursts)) * clamp(float64(m-b.from)/float64(b.to-b.from))
	}
	return done
}

// dayShare is the share of the waking day (07:00–23:00) gone by hh:mm.
func dayShare(hhmm string) float64 {
	var h, m int
	fmt.Sscanf(hhmm, "%d:%d", &h, &m)
	return clamp(float64(h*60+m-7*60) / (16 * 60))
}

func summary(o Options) []string {
	from, to := o.Today.AddDate(0, 0, -o.Days), o.Today.AddDate(0, 0, -1)
	out := []string{fmt.Sprintf("%d final days %s … %s (seed %d), snapshots for days with health", o.Days, dates.Format(from), dates.Format(to), o.Seed)}
	if o.TodayAt != "" {
		s := fmt.Sprintf("partial log for %s as of %s", dates.Format(o.Today), o.TodayAt)
		if o.TodaySteps > 0 {
			s += fmt.Sprintf(" with %d steps", o.TodaySteps)
		}
		out = append(out, s)
	}
	if o.Heavy > 0 {
		out = append(out, "heavy day: "+ago(o, o.Heavy))
	}
	if o.StreakApp != "" {
		out = append(out, fmt.Sprintf("%s streak: last %d day(s)%s", shortName(o.StreakApp), o.StreakDays, map[bool]string{true: " + today", false: ""}[o.TodayAt != ""]))
	}
	for _, n := range o.NullHealth {
		out = append(out, "health: null on "+ago(o, n))
	}
	for _, n := range o.HealthError {
		out = append(out, "health_error on "+ago(o, n))
	}
	if o.MissingApp != "" {
		out = append(out, fmt.Sprintf("%s missing from apps: for the last %d day(s)", shortName(o.MissingApp), o.MissingDays))
	}
	return append(out, "goals.md tracks "+strings.Join(o.Tracked, ", "))
}

func ago(o Options, n int) string {
	return fmt.Sprintf("%s (%d day(s) ago)", dates.Format(o.Today.AddDate(0, 0, -n)), n)
}

func save(path, header string, v any) error {
	data, err := pulse.Encode(header, v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return store.AtomicWrite(path, data)
}

func hasPkg(pkg string) bool {
	for _, u := range usual {
		if u.pkg == pkg {
			return true
		}
	}
	return false
}

// shortName: "com.google.android.youtube" → "youtube", or the fixture's display name.
func shortName(pkg string) string {
	for _, u := range usual {
		if u.pkg == pkg {
			return u.name
		}
	}
	return pkg[strings.LastIndex(pkg, ".")+1:]
}

func clock(m int) string {
	m = ((m % 1440) + 1440) % 1440
	return fmt.Sprintf("%02d:%02d", m/60, m%60)
}

func clamp(v float64) float64 { return math.Max(0, math.Min(1, v)) }
func round(v float64) int     { return int(math.Round(v)) }

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func containsStr(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
