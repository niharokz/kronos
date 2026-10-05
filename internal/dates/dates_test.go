package dates

import (
	"testing"
	"time"
)

func d(s string) time.Time {
	t, ok := ParseISO(s)
	if !ok {
		panic("bad test date " + s)
	}
	return t
}

func TestParseISO(t *testing.T) {
	for in, ok := range map[string]bool{
		"2026-10-05": true, " 2026-10-05 ": true, "20261005": true,
		"2026-1-5": false, "2026-02-30": false, "next week": false, "": false, "0000-03-14": false,
	} {
		if _, got := ParseISO(in); got != ok {
			t.Errorf("ParseISO(%q) ok=%v, want %v", in, got, ok)
		}
	}
	if got, _ := ParseTimestamp("2026-1-5"); Format(got) != "2026-01-05" {
		t.Errorf("ParseTimestamp(2026-1-5) = %s", Format(got))
	}
}

// Without an anchor the rules match Taskmaster's Python exactly.
func TestRulesMatchPython(t *testing.T) {
	cases := []struct{ from, rule, want string }{
		{"2026-01-31", "monthly", "2026-02-28"},
		{"2024-01-31", "monthly", "2024-02-29"},
		{"2026-02-28", "monthly", "2026-03-28"}, // Python drifts here (anchor fixes it)
		{"2026-11-30", "quarterly", "2027-02-28"},
		{"2024-02-29", "yearly", "2025-02-28"},
		{"2026-10-05", "daily", "2026-10-06"},
		{"2026-10-05", "weekly", "2026-10-12"},
		{"2026-10-05", "90 days", "2027-01-03"},
		{"2026-10-05", "1 day", "2026-10-06"},
		{"2026-10-05", "2 weeks", "2026-10-19"},
		{"2026-10-05", "18 months", "2028-04-05"},
		{"2026-10-05", " Monthly ", "2026-11-05"},
	}
	for _, c := range cases {
		got, ok := Advance(d(c.from), c.rule, 0)
		if !ok || Format(got) != c.want {
			t.Errorf("Advance(%s, %q) = %s,%v want %s", c.from, c.rule, Format(got), ok, c.want)
		}
	}
	for _, bad := range []string{"none", "", "fortnightly", "0 days", "-1 days", "2 fortnights", "once"} {
		if _, ok := Advance(d("2026-10-05"), bad, 0); ok {
			t.Errorf("Advance(%q) should be unknown", bad)
		}
	}
}

func TestRollForward(t *testing.T) {
	got, ok := RollForward(d("2026-09-15"), "weekly", d("2026-10-05"), 0)
	if !ok || Format(got) != "2026-10-06" {
		t.Errorf("weekly roll = %s", Format(got))
	}
	// Already on/after the target: returned unchanged even for an unknown rule (Python behaviour).
	if got, ok := RollForward(d("2026-10-05"), "bogus", d("2026-10-05"), 0); !ok || Format(got) != "2026-10-05" {
		t.Errorf("no-op roll = %s,%v", Format(got), ok)
	}
	if _, ok := RollForward(d("2026-10-01"), "bogus", d("2026-10-05"), 0); ok {
		t.Error("unknown rule in the past should fail")
	}
}

// The anchor keeps month-end items on month-end.
func TestAnchorStopsDrift(t *testing.T) {
	cur := d("2026-01-31")
	anchor := AnchorDay(cur, 0)
	var got []string
	for i := 0; i < 4; i++ {
		cur, _ = Advance(cur, "monthly", anchor)
		got = append(got, Format(cur))
	}
	want := []string{"2026-02-28", "2026-03-31", "2026-04-30", "2026-05-31"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("anchored monthly = %v, want %v", got, want)
		}
	}
	// Stored anchor 31 is honoured from a clamped date …
	if a := AnchorDay(d("2026-02-28"), 31); a != 31 {
		t.Errorf("AnchorDay(28 Feb, 31) = %d, want 31", a)
	}
	// … but ignored when the date was moved by hand to another day.
	if a := AnchorDay(d("2026-03-15"), 31); a != 15 {
		t.Errorf("stale anchor: AnchorDay(15 Mar, 31) = %d, want 15", a)
	}
	// Leap day returns in leap years.
	y, _ := Advance(d("2027-02-28"), "yearly", 29)
	if Format(y) != "2028-02-29" {
		t.Errorf("anchored yearly = %s, want 2028-02-29", Format(y))
	}
}
