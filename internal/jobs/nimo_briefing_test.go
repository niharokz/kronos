package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/niharokz/kronos/internal/dates"
)

// The Pulse brief for today is appended to the agenda; a stale one is not.
func TestBriefingAppendsPulse(t *testing.T) {
	c, dir, _ := testCtx(t, "2026-10-21", false)
	rahi := filepath.Join(dir, "rahi")
	c.Cfg.Paths["rahi"] = rahi
	_ = os.MkdirAll(filepath.Join(rahi, "state"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "outbox"), 0o755)
	put(t, filepath.Join(dir, "daily.md"), "# Overdue\n\n# Due Today\nEVENT: X\n\n# Upcoming\n")
	put(t, filepath.Join(rahi, "state", "brief.yml"), "date: '"+dates.Format(c.Today)+"'\nlines:\n  - 'Today: Steps ≥ 8,000 steps'\n")
	if err := nimoBriefing(c); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "outbox", "*.json"))
	if len(files) != 1 {
		t.Fatalf("outbox %v", files)
	}
	b, _ := os.ReadFile(files[0])
	if !strings.Contains(string(b), `EVENT: X\n\n# Upcoming\n\n# Pulse\nToday: Steps ≥ 8,000 steps`) {
		t.Errorf("briefing: %s", b)
	}
}
