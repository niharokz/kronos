package fixture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/niharokz/kronos/internal/dates"
	"gitlab.com/niharokz/kronos/internal/pulse"
)

// Every pattern lands in the logs the way Pulse reads them.
func TestWritePatterns(t *testing.T) {
	today, _ := dates.ParseISO("2026-11-30")
	out := filepath.Join(t.TempDir(), "sb")
	o := Options{Out: out, Today: today, TodayAt: "19:00", TodaySteps: 3000, Heavy: 10, StreakApp: YouTube, StreakDays: 3,
		NullHealth: []int{15}, HealthError: []int{20}, MissingApp: Instagram, MissingDays: 5, Seed: 1}
	lines, err := Write(o)
	if err != nil || len(lines) < 6 {
		t.Fatalf("%v %v", lines, err)
	}
	day := func(ago int) pulse.Day {
		d, ok, err := pulse.LoadDay(pulse.DayFile(out, today.AddDate(0, 0, -ago)))
		if !ok || err != nil {
			t.Fatalf("day %d: %v", ago, err)
		}
		return d
	}
	if files, _ := filepath.Glob(filepath.Join(out, "daily", "*.md")); len(files) != 36 {
		t.Errorf("%d logs, want 35 + today", len(files))
	}
	if d := day(15); d.Steps != nil || d.SleepMin != nil || d.ScreenMin == nil {
		t.Errorf("null-health day %+v", d)
	}
	if d := day(20); d.Steps != nil {
		t.Errorf("health_error day should count as missing: %v", *d.Steps)
	}
	if v := day(3).Value(pulse.AppMetric(Instagram)); v == nil || *v != 0 {
		t.Errorf("missing app should read as 0: %v", v)
	}
	if v := day(6).Value(pulse.AppMetric(Instagram)); v == nil || *v == 0 {
		t.Error("app missing too early")
	}
	for ago := 1; ago <= 3; ago++ {
		if v := *day(ago).Value(pulse.AppMetric(YouTube)); v < 50 {
			t.Errorf("streak day %d: YouTube %v", ago, v)
		}
	}
	if d := day(10); *d.Steps > 4000 || *d.ScreenMin < 250 {
		t.Errorf("heavy day steps %v screen %v", *d.Steps, *d.ScreenMin)
	}
	if d := day(0); d.Final || *d.Steps != 3000 {
		t.Errorf("today should be partial with 3000 steps: %+v", d)
	}

	// Snapshots: cumulative, ending near the day's total; none on days without health.
	in, err := pulse.LoadIntraday(pulse.IntradayFile(out, today.AddDate(0, 0, -1)))
	if err != nil || len(in.Snapshots) != 30 {
		t.Fatalf("snapshots %d %v", len(in.Snapshots), err)
	}
	prev := -1.0
	for _, s := range in.Snapshots {
		if *s.Steps < prev {
			t.Fatalf("steps go down at %s", s.At)
		}
		prev = *s.Steps
	}
	if total := *day(1).Steps; prev > total || prev < total*0.95 { // 22:30 is close to the day's end
		t.Errorf("last snapshot %v, day total %v", prev, total)
	}
	if _, err := os.Stat(pulse.IntradayFile(out, today.AddDate(0, 0, -15))); err == nil {
		t.Error("snapshot on a null-health day")
	}
	g, ok, err := pulse.LoadGoals(pulse.GoalsFile(out))
	if !ok || err != nil || strings.Join(g.TrackedApps, ",") != Instagram+","+YouTube {
		t.Errorf("goals %+v %v", g.TrackedApps, err)
	}
}

// Same seed, same logs; --force only replaces an earlier fixture.
func TestWriteDeterministicAndSafe(t *testing.T) {
	today, _ := dates.ParseISO("2026-11-30")
	out := filepath.Join(t.TempDir(), "sb")
	read := func() string {
		b, _ := os.ReadFile(pulse.DayFile(out, today.AddDate(0, 0, -7)))
		return string(b)
	}
	if _, err := Write(Options{Out: out, Today: today, Days: 10}); err != nil {
		t.Fatal(err)
	}
	first := read()
	if _, err := Write(Options{Out: out, Today: today, Days: 10}); err == nil {
		t.Error("overwrote without --force")
	}
	if _, err := Write(Options{Out: out, Today: today, Days: 10, Force: true}); err != nil || read() != first {
		t.Errorf("not deterministic: %v", err)
	}
	vault := t.TempDir()
	_ = os.WriteFile(filepath.Join(vault, "note.md"), []byte("mine"), 0o644)
	if _, err := Write(Options{Out: vault, Today: today, Force: true}); err == nil {
		t.Error("wiped a folder that isn't a fixture")
	}
	if _, err := os.Stat(filepath.Join(vault, "note.md")); err != nil {
		t.Error("vault file gone")
	}
}
