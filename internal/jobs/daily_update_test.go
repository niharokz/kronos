package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/niharokz/kronos/internal/dates"
)

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
