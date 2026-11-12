package kshan

import (
	"testing"
	"time"
)

// Thu 12 Nov 2026 is the base day.
func TestParseWhen(t *testing.T) {
	base := time.Date(2026, 11, 12, 19, 30, 0, 0, time.Local)
	for text, want := range map[string]When{
		"dentist fri 5pm":                         {"Dentist", day(2026, 11, 13), "17:00"},
		"call mom tomorrow":                       {"Call mom", day(2026, 11, 13), ""},
		"standup today at 9:30am":                 {"Standup", day(2026, 11, 12), "09:30"},
		"gym thu":                                 {"Gym", day(2026, 11, 12), ""}, // today is Thursday
		"gym next thu":                            {"Gym", day(2026, 11, 19), ""},
		"rent 1 dec":                              {"Rent", day(2026, 12, 1), ""},
		"birthday party nov 3":                    {"Birthday party", day(2027, 11, 3), ""}, // past this year → next
		"flight 2026-12-20 06:30":                 {"Flight", day(2026, 12, 20), "06:30"},
		"pay bill 15/12":                          {"Pay bill", day(2026, 12, 15), ""},
		"meet arun on 14th november 2027 at 6 pm": {"Meet arun", day(2027, 11, 14), "18:00"},
		"buy 2 milk":                              {"Buy 2 milk", day(2026, 11, 12), ""}, // a bare number is not a time
		"lunch noon":                              {"Lunch", day(2026, 11, 12), "12:00"},
		"tomorrow":                                {"Tomorrow", day(2026, 11, 13), ""},
	} {
		got := ParseWhen(text, base)
		if got.Title != want.Title || !got.Date.Equal(want.Date) || got.Time != want.Time {
			t.Errorf("%q: got %q %s %q, want %q %s %q", text, got.Title, got.Date.Format("2006-01-02"), got.Time,
				want.Title, want.Date.Format("2006-01-02"), want.Time)
		}
	}
}

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.Local) }
