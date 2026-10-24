package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"gitlab.com/niharokz/kronos/internal/dates"
	"gitlab.com/niharokz/kronos/internal/pulse/fixture"
)

const fixtureUsage = `kronos pulse-fixture — write fake Rahi logs for testing Pulse

  kronos pulse-fixture --out DIR [options]

Then: kronos run pulse_rollup --rahi-path DIR --now 'YYYY-MM-DD HH:MM' --dry-run
Day offsets count back from --today (1 = yesterday).

Options:`

// pulseFixture is `kronos pulse-fixture`.
func pulseFixture(args []string) int {
	fs := flag.NewFlagSet("pulse-fixture", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(fs.Output(), fixtureUsage); fs.PrintDefaults() }
	var o fixture.Options
	fs.StringVar(&o.Out, "out", "", "sandbox folder (created; must be empty or an earlier fixture)")
	today := fs.String("today", "", "the day you'll pass to --now, YYYY-MM-DD (default today)")
	fs.IntVar(&o.Days, "days", 35, "final days of logs before --today")
	fs.StringVar(&o.TodayAt, "today-at", "", "also write a partial log for --today as of HH:MM")
	fs.IntVar(&o.TodaySteps, "today-steps", 0, "steps so far in that partial log (default: a normal pace)")
	fs.IntVar(&o.Heavy, "heavy", 0, "one-off heavy day N days ago (lots of Instagram/YouTube, few steps)")
	streak := fs.String("streak", "", "app streak ending yesterday, pkg:days (e.g. "+fixture.YouTube+":3)")
	nullH := fs.String("null-health", "", "days with health: null, e.g. 15,16")
	errH := fs.String("health-error", "", "days with health_error set, e.g. 20")
	missing := fs.String("missing-app", "", "tracked app absent from apps: for the last N days, pkg:days")
	tracked := fs.String("tracked", "", "goals.md tracked_apps, comma-separated (default Instagram, YouTube)")
	fs.Int64Var(&o.Seed, "seed", 1, "random seed (same seed, same logs)")
	fs.BoolVar(&o.Force, "force", false, "replace an earlier fixture in --out")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var err error
	if *today != "" {
		t, ok := dates.ParseISO(*today)
		if !ok {
			return fixtureErr("--today %q: want YYYY-MM-DD", *today)
		}
		o.Today = t
	}
	if o.StreakApp, o.StreakDays, err = pkgDays(*streak); err != nil {
		return fixtureErr("--streak: %v", err)
	}
	if o.MissingApp, o.MissingDays, err = pkgDays(*missing); err != nil {
		return fixtureErr("--missing-app: %v", err)
	}
	if o.NullHealth, err = ints(*nullH); err != nil {
		return fixtureErr("--null-health: %v", err)
	}
	if o.HealthError, err = ints(*errH); err != nil {
		return fixtureErr("--health-error: %v", err)
	}
	for _, p := range strings.Split(*tracked, ",") {
		if p = strings.TrimSpace(p); p != "" {
			o.Tracked = append(o.Tracked, p)
		}
	}

	lines, err := fixture.Write(o)
	if err != nil {
		return fixtureErr("%v", err)
	}
	fmt.Println("Pulse fixture in", o.Out)
	for _, l := range lines {
		fmt.Println("  " + l)
	}
	return 0
}

// pkgDays parses "pkg:N" ("" is none).
func pkgDays(s string) (string, int, error) {
	if s == "" {
		return "", 0, nil
	}
	pkg, n, ok := strings.Cut(s, ":")
	days, err := strconv.Atoi(n)
	if !ok || pkg == "" || err != nil || days < 1 {
		return "", 0, fmt.Errorf("%q: want pkg:days", s)
	}
	return pkg, days, nil
}

// ints parses "15,16".
func ints(s string) ([]int, error) {
	var out []int
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("%q: want day numbers ≥ 1", p)
		}
		out = append(out, n)
	}
	return out, nil
}

func fixtureErr(format string, a ...any) int {
	fmt.Fprintf(os.Stderr, "kronos pulse-fixture: "+format+"\n", a...)
	return 2
}
