package jobs

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/niharokz/kronos/internal/config"
	"gitlab.com/niharokz/kronos/internal/dates"
	"gitlab.com/niharokz/kronos/internal/job"
	"gitlab.com/niharokz/kronos/internal/store"
)

// Behaviour shared with Taskmaster is covered by the parity run against the
// Python code (see ARCHITECTURE.md). These tests cover what is new.

func testCtx(t *testing.T, today string, dry bool) (*job.Ctx, string, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.Parse([]byte(`paths:
  logs: `+dir+`/logs
  state: `+dir+`/state.json
  outbox: `+dir+`/outbox
  events: `+dir+`/event.md
  event_backup: `+dir+`/event.md.bck
  subscriptions: `+dir+`/subscription.md
  birthdays: `+dir+`/birthdays.md
  daily: `+dir+`/daily.md
`), "test.yml")
	if err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	c := job.NewCtx(context.Background(), cfg, config.Job{Name: "test"}, dry, out)
	c.Today, _ = dates.ParseISO(today)
	return c, dir, out
}

func put(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func eventByID(t *testing.T, path, id string) map[string]string {
	t.Helper()
	recs, err := store.LoadRecords(path, "events", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if store.Field(r, "id") == id {
			out := map[string]string{}
			for i := 0; i+1 < len(r.Content); i += 2 {
				out[r.Content[i].Value] = r.Content[i+1].Value
			}
			return out
		}
	}
	t.Fatalf("event %s not found", id)
	return nil
}

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

func TestMissedDayStillCreatesSubscriptionEvent(t *testing.T) {
	// Host off on the due day (Oct 3); catch-up on Oct 5 for a 2-day cycle lands on today.
	c, dir, _ := testCtx(t, "2026-10-05", false)
	put(t, filepath.Join(dir, "event.md"), "events:\n- {id: x, title: X, date: '2026-12-01', status: open, frequency: none, priority: low}\n")
	put(t, filepath.Join(dir, "subscription.md"), "subscriptions:\n- {name: Pills Box, status: active, next_due_date: '2026-10-03', cycle: 2 days}\n")
	if err := subscriptionUpdate(c); err != nil {
		t.Fatal(err)
	}
	e := eventByID(t, filepath.Join(dir, "event.md"), "sub-pills-box-2026-10-05")
	if e["status"] != "pending" || e["title"] != "Subscription due: Pills Box" {
		t.Errorf("got %v", e)
	}
	// Running again must not duplicate it.
	if err := subscriptionUpdate(c); err != nil {
		t.Fatal(err)
	}
	recs, _ := store.LoadRecords(filepath.Join(dir, "event.md"), "events", true)
	if len(recs) != 2 {
		t.Errorf("events = %d, want 2 (no duplicate)", len(recs))
	}
}

func TestBirthdayAcrossNewYear(t *testing.T) {
	today, _ := dates.ParseISO("2026-12-28")
	if got := nextBirthday(1, 2, today); dates.Format(got) != "2027-01-02" {
		t.Errorf("next = %s", dates.Format(got))
	}
	if got := nextBirthday(2, 29, today); dates.Format(got) != "2027-02-28" {
		t.Errorf("leap = %s", dates.Format(got))
	}
}

func TestAgendaPartialFailureStillWrites(t *testing.T) {
	c, dir, _ := testCtx(t, "2026-10-05", false)
	put(t, filepath.Join(dir, "event.md"), "events: [ {broken\n")
	put(t, filepath.Join(dir, "birthdays.md"), "people:\n- {name: Asha, birthday: '0000-10-05'}\n")
	err := dailyUpdate(c)
	if err == nil || !strings.Contains(err.Error(), "unreadable: event.md") {
		t.Fatalf("err = %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "daily.md"))
	if string(raw) != "# Overdue\n\n# Due Today\nBIRTHDAY- Asha\n\n# Upcoming\n" {
		t.Errorf("daily.md = %q", raw)
	}
}

func TestEveryBuiltInJobIsRegistered(t *testing.T) {
	want := []string{"archive_rollover", "arthik_subscriptions", "birthday_sync", "daily_update", "event_archive", "event_update",
		"health_website", "inbox_archive", "nimo_briefing", "restarted", "subscription_update", "untitled_import"}
	got := strings.Join(job.Names(), ",")
	if got != strings.Join(want, ",") {
		t.Errorf("registered = %s", got)
	}
}
