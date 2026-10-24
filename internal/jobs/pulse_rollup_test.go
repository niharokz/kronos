package jobs

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.com/niharokz/kronos/internal/dates"
	"gitlab.com/niharokz/kronos/internal/job"
	"gitlab.com/niharokz/kronos/internal/pulse"
)

// pulseFixture writes `n` final Rahi days ending yesterday and returns the rahi dir.
// Screen 200 min, Instagram 30 min, steps 8000, sleep 420 — except the last `bad` days.
func pulseFixture(t *testing.T, c *job.Ctx, root string, n, bad int) string {
	t.Helper()
	dir := filepath.Join(root, "rahi")
	c.Cfg.Paths["rahi"] = dir
	c.Cfg.Paths["outbox"] = filepath.Join(root, "outbox")
	_ = os.MkdirAll(filepath.Join(dir, "daily"), 0o755)
	_ = os.MkdirAll(c.Cfg.Paths["outbox"], 0o755)
	put(t, filepath.Join(dir, "goals.md"), "tracked_apps: [com.instagram.android]\nquiet_hours: { start: '23:00', end: '06:00' }\n")
	for i := n; i >= 1; i-- {
		d := c.Today.AddDate(0, 0, -i)
		screen, insta := 200, 30
		if i <= bad {
			screen, insta = 320, 80
		}
		writeRahiDay(t, dir, d, true, screen, insta, 8000)
	}
	return dir
}

func writeRahiDay(t *testing.T, dir string, d time.Time, final bool, screen, insta, steps int) {
	t.Helper()
	put(t, pulse.DayFile(dir, d), fmt.Sprintf(`# Rahi daily log
date: '%s'
final: %v
health: { steps: %d, sleep: { start: '23:30', end: '06:30', min: 420 } }
screen: { total_min: %d, unlocks: 60, first_use: '07:00', last_use: '23:00' }
apps:
  - { pkg: com.instagram.android, name: Instagram, min: %d, opens: 9 }
`, dates.Format(d), final, steps, screen, insta))
}

func dailyHashes(t *testing.T, dir string) string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "daily", "*.md"))
	h := sha256.New()
	for _, f := range files {
		b, _ := os.ReadFile(f)
		h.Write(b)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// Ten normal days then three bad ones: goals start, a pattern warning goes to Telegram once,
// the brief carries targets, daily/ is never touched, and a rerun adds nothing.
func TestPulseRollup(t *testing.T) {
	c, root, _ := testCtx(t, "2026-10-21", false) // a Wednesday
	c.Now = c.Today.Add(9 * time.Hour)
	dir := pulseFixture(t, c, root, 14, 3)
	before := dailyHashes(t, dir)

	if err := pulseRollup(c); err != nil {
		t.Fatal(err)
	}
	st, err := pulse.LoadState(pulse.BaselineFile(dir))
	if err != nil || !st.Ready() || st.Metrics["screen"].Goal == nil || len(st.RolledDays) != 14 {
		t.Fatalf("state %+v err %v", st, err)
	}
	if st.Metrics["screen"].Goal.Start != 200 {
		t.Errorf("goal starts at the 28-day median: %v", st.Metrics["screen"].Goal.Start)
	}
	outbox, _ := filepath.Glob(filepath.Join(root, "outbox", "*.json"))
	var warned bool
	for _, f := range outbox {
		b, _ := os.ReadFile(f)
		warned = warned || strings.Contains(string(b), "Screen time has been well over your usual")
	}
	if !warned {
		t.Errorf("no pattern warning in outbox (%d files)", len(outbox))
	}
	b, ok := pulse.LoadBrief(pulse.BriefFile(dir), c.Today)
	if !ok || !strings.Contains(b.Text(), "Yesterday: Screen time 320 ✗") || !strings.Contains(b.Text(), "Today: Screen time ≤ 288 min") {
		t.Errorf("brief:\n%s", b.Text())
	}
	if dailyHashes(t, dir) != before {
		t.Fatal("Kronos modified Rahi's daily logs")
	}

	n := len(outbox)
	if err := pulseRollup(c); err != nil {
		t.Fatal(err)
	}
	if again, _ := filepath.Glob(filepath.Join(root, "outbox", "*.json")); len(again) != n {
		t.Error("rerun sent again")
	}
}

// During quiet hours a pattern warning is deferred to the next briefing, not dropped.
func TestPulseRollupDefersInQuietHours(t *testing.T) {
	c, root, _ := testCtx(t, "2026-10-21", false)
	c.Now = c.Today.Add(23*time.Hour + 30*time.Minute)
	dir := pulseFixture(t, c, root, 14, 3)
	if err := pulseRollup(c); err != nil {
		t.Fatal(err)
	}
	if out, _ := filepath.Glob(filepath.Join(root, "outbox", "*.json")); len(out) != 0 {
		t.Fatalf("sent during quiet hours: %v", out)
	}
	sent, _ := pulse.LoadSent(pulse.SentFile(dir))
	if d := sent.Deferred(c.Today.AddDate(0, 0, 1)); len(d) == 0 {
		t.Fatal("warning not deferred to tomorrow's briefing")
	}
	c.Today, c.Now = c.Today.AddDate(0, 0, 1), c.Now.Add(6*time.Hour) // next morning, 05:30
	if err := pulseRollup(c); err != nil {
		t.Fatal(err)
	}
	if b, ok := pulse.LoadBrief(pulse.BriefFile(dir), c.Today); !ok || !strings.Contains(b.Text(), "⚠ Screen time has been well over") {
		t.Errorf("deferred warning missing from brief:\n%s", b.Text())
	}
}

// Sunday final → weekly rollup file and a report (Telegram + short inbox version).
func TestPulseWeeklyRollup(t *testing.T) {
	c, root, _ := testCtx(t, "2026-10-26", false) // Monday
	c.Now = c.Today.Add(10 * time.Hour)
	dir := pulseFixture(t, c, root, 21, 0)
	if err := pulseRollup(c); err != nil {
		t.Fatal(err)
	}
	r, ok := loadRollup(pulse.WeeklyFile(dir, "2026-W43"))
	if !ok || r.DaysLogged != 7 || *r.Metrics["screen"].Median != 200 {
		t.Fatalf("weekly rollup %+v", r)
	}
	if _, err := os.Stat(pulse.InboxFile(dir, "week-2026-w43")); err != nil {
		t.Error("short weekly report not in the inbox")
	}
	sent, _ := pulse.LoadSent(pulse.SentFile(dir))
	if !sent.Has("report-weekly-2026-W43") {
		t.Error("weekly report not sent")
	}
}

// Observe-only: fewer than 7 days → no goals, no messages, a "learning" brief.
func TestPulseObserveOnly(t *testing.T) {
	c, root, _ := testCtx(t, "2026-10-21", false)
	c.Now = c.Today.Add(9 * time.Hour)
	dir := pulseFixture(t, c, root, 4, 2)
	if err := pulseRollup(c); err != nil {
		t.Fatal(err)
	}
	st, _ := pulse.LoadState(pulse.BaselineFile(dir))
	if st.Ready() || st.Metrics["screen"].Goal != nil {
		t.Error("goals before 7 days")
	}
	if b, _ := pulse.LoadBrief(pulse.BriefFile(dir), c.Today); !strings.Contains(b.Text(), "learning: 4 of 7") {
		t.Errorf("brief: %s", b.Text())
	}
}
