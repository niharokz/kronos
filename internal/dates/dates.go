// Package dates holds the calendar rules every Kronos job shares: parsing,
// month/year arithmetic and repeat rules ("monthly", "90 days", ...).
//
// The rules are a 1:1 port of Taskmaster's config/common.py, with one
// addition: an optional anchor day. Without it, a monthly item on the 31st
// drifts for good after the first short month (31 Jan → 28 Feb → 28 Mar).
// With an anchor of 31 it goes 31 Jan → 28 Feb → 31 Mar. Passing anchor 0
// gives exactly the old Python behaviour.
//
// Dates are civil dates: midnight UTC time.Time values, so arithmetic never
// trips over time zones or DST.
package dates

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Layout is the on-disk date format (YYYY-MM-DD).
const Layout = "2006-01-02"

// New returns the civil date y-m-d.
func New(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// Today returns today's civil date in the local time zone.
func Today(now time.Time) time.Time {
	y, m, d := now.Date()
	return New(y, m, d)
}

// Format renders a civil date as YYYY-MM-DD.
func Format(d time.Time) string { return d.Format(Layout) }

// Max is the sort key Python used for unparseable dates (date.max).
var Max = New(9999, 12, 31)

var (
	isoDash   = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})$`)
	isoBasic  = regexp.MustCompile(`^(\d{4})(\d{2})(\d{2})$`)
	looseDate = regexp.MustCompile(`^(\d{4})-(\d{1,2})-(\d{1,2})`)
)

// ParseISO parses what Python's date.fromisoformat accepts for our data:
// "YYYY-MM-DD" or "YYYYMMDD" (surrounding spaces ignored). Invalid dates
// such as 2026-02-30 are rejected.
func ParseISO(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	m := isoDash.FindStringSubmatch(s)
	if m == nil {
		m = isoBasic.FindStringSubmatch(s)
	}
	if m == nil {
		return time.Time{}, false
	}
	return build(m[1], m[2], m[3])
}

// ParseTimestamp parses an unquoted YAML timestamp such as 2026-1-5 or
// "2026-10-05 10:00:00", keeping only the date (Python's datetime.date()).
func ParseTimestamp(s string) (time.Time, bool) {
	m := looseDate.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return time.Time{}, false
	}
	return build(m[1], m[2], m[3])
}

func build(ys, ms, ds string) (time.Time, bool) {
	y, _ := strconv.Atoi(ys)
	mo, _ := strconv.Atoi(ms)
	d, _ := strconv.Atoi(ds)
	if y < 1 || mo < 1 || mo > 12 || d < 1 || d > DaysIn(y, time.Month(mo)) {
		return time.Time{}, false
	}
	return New(y, time.Month(mo), d), true
}

// DaysIn returns the number of days in month m of year y.
func DaysIn(y int, m time.Month) int {
	return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// AddMonths adds n months, clamping to the target month's last day.
// anchor is the day of month to aim for (0 = d's own day, the Python
// behaviour).
func AddMonths(d time.Time, n, anchor int) time.Time {
	if anchor <= 0 {
		anchor = d.Day()
	}
	total := int(d.Month()) - 1 + n
	y := d.Year() + floorDiv(total, 12)
	m := time.Month(floorMod(total, 12) + 1)
	return New(y, m, min(anchor, DaysIn(y, m)))
}

// AddYears adds n years; 29 Feb becomes 28 Feb in non-leap years (and
// returns to 29 Feb in leap years when anchored on 29).
func AddYears(d time.Time, n, anchor int) time.Time {
	return AddMonths(d, 12*n, anchor)
}

func floorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

func floorMod(a, b int) int { return a - floorDiv(a, b)*b }

// Rule is a parsed repeat rule: N units of days | weeks | months | years.
type Rule struct {
	N    int
	Unit string
}

var named = map[string]Rule{
	"daily":     {1, "days"},
	"weekly":    {1, "weeks"},
	"monthly":   {1, "months"},
	"quarterly": {3, "months"},
	"yearly":    {1, "years"},
}

// ParseRule parses daily | weekly | monthly | quarterly | yearly or
// "N days|weeks|months|years" (singular accepted). "none", empty and
// anything else is not a rule.
func ParseRule(s string) (Rule, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if r, ok := named[s]; ok {
		return r, true
	}
	parts := strings.Fields(s)
	if len(parts) != 2 || !allDigits(parts[0]) {
		return Rule{}, false
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil || n < 1 {
		return Rule{}, false
	}
	unit := strings.TrimRight(parts[1], "s") + "s" // "1 day" == "1 days"
	switch unit {
	case "days", "weeks", "months", "years":
		return Rule{n, unit}, true
	}
	return Rule{}, false
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// MonthBased reports whether the rule steps by months or years, the only
// rules where an anchor day matters.
func (r Rule) MonthBased() bool { return r.Unit == "months" || r.Unit == "years" }

// Step returns d advanced by one occurrence of the rule.
func (r Rule) Step(d time.Time, anchor int) time.Time {
	switch r.Unit {
	case "days":
		return d.AddDate(0, 0, r.N)
	case "weeks":
		return d.AddDate(0, 0, 7*r.N)
	case "months":
		return AddMonths(d, r.N, anchor)
	default: // years
		return AddYears(d, r.N, anchor)
	}
}

// Advance returns the next date for a rule string; false if the rule is
// unknown (including "none"). Python: advance().
func Advance(d time.Time, rule string, anchor int) (time.Time, bool) {
	r, ok := ParseRule(rule)
	if !ok {
		return time.Time{}, false
	}
	return r.Step(d, anchor), true
}

// RollForward advances d by rule until it is on or after until. A date
// already on/after until is returned unchanged even for an unknown rule,
// exactly like Python's roll_forward().
func RollForward(d time.Time, rule string, until time.Time, anchor int) (time.Time, bool) {
	for d.Before(until) {
		next, ok := Advance(d, rule, anchor)
		if !ok {
			return time.Time{}, false
		}
		d = next
	}
	return d, true
}

// AnchorDay returns the day of month month/year rules should aim for.
// A stored anchor (29–31) is used only while the current date still
// matches it, i.e. current day == min(anchor, days in current month); if
// the date was moved by hand to another day, the anchor is stale and the
// current day wins.
func AnchorDay(current time.Time, stored int) int {
	if stored >= 1 && stored <= 31 &&
		current.Day() == min(stored, DaysIn(current.Year(), current.Month())) {
		return stored
	}
	return current.Day()
}
