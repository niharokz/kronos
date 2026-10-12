package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/niharokz/kronos/internal/dates"
)

func TestMonthEndEventDoesNotDrift(t *testing.T) {
	c, dir, _ := testCtx(t, "2026-02-01", false)
	path := filepath.Join(dir, "event.md")
	put(t, path, "events:\n- {id: rent, title: Rent, date: '2026-01-31', status: closed, frequency: monthly, priority: high}\n")

	steps := []struct{ today, want, anchor string }{
		{"2026-02-01", "2026-02-28", "31"}, // reopened at month end, anchor remembered
		{"2026-03-01", "2026-03-31", "31"}, // back to the 31st (Python: 2026-03-28)
		{"2026-04-01", "2026-04-30", "31"},
	}
	for _, s := range steps {
		c.Today, _ = dates.ParseISO(s.today)
		if err := eventUpdate(c); err != nil {
			t.Fatal(err)
		}
		e := eventByID(t, path, "rent")
		if e["date"] != s.want || e["anchor_day"] != s.anchor || e["status"] != "open" {
			t.Fatalf("on %s: %v, want date %s anchor %s", s.today, e, s.want, s.anchor)
		}
		// Close it again for the next month.
		raw, _ := os.ReadFile(path)
		put(t, path, strings.Replace(string(raw), "status: open", "status: closed", 1))
	}
}

func TestAnchorNotAddedForOrdinaryDays(t *testing.T) {
	c, dir, _ := testCtx(t, "2026-10-05", false)
	path := filepath.Join(dir, "event.md")
	put(t, path, "events:\n- {id: gym, title: Gym fee, date: '2026-09-10', status: open, frequency: monthly, priority: low}\n")
	if err := eventUpdate(c); err != nil {
		t.Fatal(err)
	}
	e := eventByID(t, path, "gym")
	if e["date"] != "2026-10-10" || e["anchor_day"] != "" {
		t.Errorf("got %v", e)
	}
}

func TestLegacyValuesNormalised(t *testing.T) {
	c, dir, _ := testCtx(t, "2026-10-05", false)
	path := filepath.Join(dir, "event.md")
	put(t, path, "events:\n- {id: old, title: Old bot event, date: '2026-10-01', status: open, frequency: once, priority: normal}\n")
	if err := eventUpdate(c); err != nil {
		t.Fatal(err)
	}
	e := eventByID(t, path, "old")
	// "once" is one-time → overdue moves to today as pending (Python treated it as an unknown rule).
	if e["frequency"] != "none" || e["priority"] != "medium" || e["status"] != "pending" || e["date"] != "2026-10-05" {
		t.Errorf("got %v", e)
	}
}

func TestBrokenEventFileIsNeverOverwritten(t *testing.T) {
	c, dir, _ := testCtx(t, "2026-10-05", false)
	path := filepath.Join(dir, "event.md")
	broken := "events:\n- id: a\n  title: [unclosed\n"
	put(t, path, broken)
	if err := eventUpdate(c); err == nil || !strings.Contains(err.Error(), "Invalid YAML in event.md") {
		t.Fatalf("err = %v", err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != broken {
		t.Error("broken file was modified")
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	c, dir, out := testCtx(t, "2026-10-05", true)
	path := filepath.Join(dir, "event.md")
	before := "events:\n- {id: a, title: A, date: '2026-10-01', status: open, frequency: none, priority: low}\n"
	put(t, path, before)
	if err := eventUpdate(c); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != before {
		t.Error("dry run modified the file")
	}
	if !strings.Contains(out.String(), "~ would change") || !strings.Contains(out.String(), "+     status: pending") {
		t.Errorf("dry-run diff missing:\n%s", out.String())
	}
}
