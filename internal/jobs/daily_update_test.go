package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgendaPartialFailureStillWrites(t *testing.T) {
	c, dir, _ := testCtx(t, "2026-10-05", false)
	put(t, filepath.Join(dir, "event.md"), "events: [ {broken\n")
	put(t, filepath.Join(dir, "calendar.md"), "events:\n- {title: Asha's birthday, date: '2026-10-05', category: birthday}\n")
	err := dailyUpdate(c)
	if err == nil || !strings.Contains(err.Error(), "unreadable: event.md") {
		t.Fatalf("err = %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "daily.md"))
	if string(raw) != "# Overdue\n\n# Due Today\nBIRTHDAY- Asha\n\n# Upcoming\n" {
		t.Errorf("daily.md = %q", raw)
	}
}

// Calendar entries: today (incl. a multi-day event still running), upcoming with date, past ignored.
func TestAgendaCalendarEntries(t *testing.T) {
	c, dir, _ := testCtx(t, "2026-10-05", false)
	put(t, filepath.Join(dir, "calendar.md"), `events:
- {title: Old trip, date: '2026-10-01', end: '2026-10-03', category: event}
- {title: Conference, date: '2026-10-04', end: '2026-10-06', category: event}
- {title: Dentist, date: '2026-10-05', time: '10:30', category: event}
- {title: Diwali, date: '2026-10-08', category: holiday}
- {title: Ravi’s birthday, date: '2026-10-09', category: birthday}
- {title: Far away, date: '2026-11-30', category: event}
`)
	if err := dailyUpdate(c); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "daily.md"))
	want := "# Overdue\n\n# Due Today\nCALENDAR- Conference\nCALENDAR- Dentist 10:30\n\n" +
		"# Upcoming\nBIRTHDAY- Ravi (2026-10-09)\nHOLIDAY- Diwali (2026-10-08)\n"
	if string(raw) != want {
		t.Errorf("daily.md =\n%s\nwant\n%s", raw, want)
	}
}
