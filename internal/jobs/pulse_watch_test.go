package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.com/niharokz/kronos/internal/pulse"
)

// An app over its 7-day median while on a strike pattern → one inbox nudge per day; a snapshot each run.
func TestPulseWatchAppNudge(t *testing.T) {
	c, root, out := testCtx(t, "2026-10-21", false)
	c.Now = c.Today.Add(9 * time.Hour)
	dir := pulseFixture(t, c, root, 14, 3)
	if err := pulseRollup(c); err != nil {
		t.Fatal(err)
	}
	writeRahiDay(t, dir, c.Today, false, 150, 95, 9000)
	c.Now = c.Today.Add(18 * time.Hour)
	if err := pulseWatch(c); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(pulse.InboxFile(dir, "nudge-instagram-20261021"))
	if err != nil {
		t.Fatal("no app nudge in the inbox\n" + out.String())
	}
	if s := string(raw); !strings.HasPrefix(s, "---\nid: nudge-instagram-20261021\ntitle: Instagram — 95 min today\nfrom: Kronos\n") ||
		!strings.Contains(s, "over on 3 of the last 7 days") {
		t.Errorf("nudge:\n%s", s)
	}
	in, _ := pulse.LoadIntraday(pulse.IntradayFile(dir, c.Today))
	if len(in.Snapshots) != 1 || *in.Snapshots[0].Steps != 9000 || in.Snapshots[0].Apps["com.instagram.android"] != 95 {
		t.Errorf("snapshot %+v", in)
	}
	_ = os.Remove(pulse.InboxFile(dir, "nudge-instagram-20261021")) // Rahi moved it to read/
	c.Now = c.Now.Add(30 * time.Minute)
	if err := pulseWatch(c); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pulse.InboxFile(dir, "nudge-instagram-20261021")); err == nil {
		t.Error("same app nudged twice in a day")
	}
}

// Steps far behind late in the day → the final nudge; silent once the target is reached.
func TestPulseWatchStepNudge(t *testing.T) {
	c, root, _ := testCtx(t, "2026-10-21", false)
	c.Now = c.Today.Add(9 * time.Hour)
	dir := pulseFixture(t, c, root, 14, 0)
	if err := pulseRollup(c); err != nil {
		t.Fatal(err)
	}
	writeRahiDay(t, dir, c.Today, false, 150, 20, 2000)
	c.Now = c.Today.Add(20*time.Hour + 45*time.Minute)
	if err := pulseWatch(c); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(pulse.InboxFile(dir, "steps-final-20261021"))
	if err != nil || !strings.Contains(string(raw), "Last call: 6,000 steps to go") {
		t.Fatalf("final step nudge: %s %v", raw, err)
	}

	c2, root2, _ := testCtx(t, "2026-10-21", false)
	c2.Now = c2.Today.Add(9 * time.Hour)
	dir2 := pulseFixture(t, c2, root2, 14, 0)
	_ = pulseRollup(c2)
	writeRahiDay(t, dir2, c2.Today, false, 150, 20, 8500)
	c2.Now = c2.Today.Add(21 * time.Hour)
	_ = pulseWatch(c2)
	if files, _ := filepath.Glob(filepath.Join(dir2, "inbox", "steps-*")); len(files) != 0 {
		t.Errorf("nudged after the goal was hit: %v", files)
	}
}

// Before 7 days: snapshot only, no nudges.
func TestPulseWatchObserveOnly(t *testing.T) {
	c, root, _ := testCtx(t, "2026-10-21", false)
	c.Now = c.Today.Add(9 * time.Hour)
	dir := pulseFixture(t, c, root, 3, 3)
	_ = pulseRollup(c)
	writeRahiDay(t, dir, c.Today, false, 400, 200, 100)
	c.Now = c.Today.Add(21 * time.Hour)
	if err := pulseWatch(c); err != nil {
		t.Fatal(err)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "inbox", "*.md")); len(files) != 0 {
		t.Errorf("nudged while observing: %v", files)
	}
	if _, err := os.Stat(pulse.IntradayFile(dir, c.Today)); err != nil {
		t.Error("no snapshot while observing")
	}
}

// A half-written log is a warning and a retry next run, never a crash.
func TestPulseWatchToleratesBadLog(t *testing.T) {
	c, root, _ := testCtx(t, "2026-10-21", false)
	c.Now = c.Today.Add(9 * time.Hour)
	dir := pulseFixture(t, c, root, 8, 0)
	put(t, pulse.DayFile(dir, c.Today), "date: '2026-10-21'\nscreen: { total_min: [unclosed\n")
	if err := pulseWatch(c); err != nil {
		t.Fatalf("should warn, not fail: %v", err)
	}
}

// --dry-run in a sandbox: messages are printed with priority, channel and id; nothing reaches
// the inbox or outbox, but Pulse's own files (baseline, snapshots, sent) are written.
// A dry run on the real vault writes nothing at all.
func TestPulseDryRun(t *testing.T) {
	c, root, out := testCtx(t, "2026-10-21", true)
	c.Sandbox = true
	c.Now = c.Today.Add(9 * time.Hour)
	dir := pulseFixture(t, c, root, 14, 3)
	if err := pulseRollup(c); err != nil {
		t.Fatal(err)
	}
	writeRahiDay(t, dir, c.Today, false, 150, 95, 9000)
	c.Now = c.Today.Add(18 * time.Hour)
	if err := pulseWatch(c); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"~ would send [p0] telegram pattern-", "~ would send [p2] inbox    nudge-instagram-20261021 — Instagram — 95 min today"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "inbox", "*")); len(files) != 0 {
		t.Errorf("inbox written in a dry run: %v", files)
	}
	if files, _ := filepath.Glob(filepath.Join(c.Cfg.Paths["outbox"], "*")); len(files) != 0 {
		t.Errorf("outbox written in a dry run: %v", files)
	}
	for _, p := range []string{pulse.BaselineFile(dir), pulse.SentFile(dir), pulse.IntradayFile(dir, c.Today), pulse.BriefFile(dir)} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("sandbox file not written: %v", err)
		}
	}

	c2, root2, _ := testCtx(t, "2026-10-21", true) // no sandbox
	c2.Now = c2.Today.Add(9 * time.Hour)
	dir2 := pulseFixture(t, c2, root2, 14, 3)
	_ = pulseRollup(c2)
	if _, err := os.Stat(pulse.BaselineFile(dir2)); err == nil {
		t.Error("dry run on real data wrote baseline.md")
	}
}
