package kshan

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// When is what a draft's text says: the title left over, the date, and a time if one was typed.
type When struct {
	Title string
	Date  time.Time
	Time  string // "HH:MM" or ""
}

// ParseWhen reads "dentist fri 5pm", "call mom tomorrow", "rent 1 dec", "flight 2026-12-20 06:30".
// Relative words count from base (the day the draft was made). No date → base. A day/month
// that is already past in base's year means next year.
func ParseWhen(text string, base time.Time) When {
	base = time.Date(base.Year(), base.Month(), base.Day(), 0, 0, 0, 0, base.Location())
	words := strings.Fields(text)
	used := make([]bool, len(words))
	w := When{Date: base}
	gotDate := false

	for i := 0; i < len(words); i++ {
		if !gotDate {
			if d, n, ok := dateAt(words, i, base); ok {
				w.Date, gotDate = d, true
				markUsed(used, words, i, n)
				i += n - 1
				continue
			}
		}
		if w.Time == "" {
			if t, n, ok := timeAt(words, i); ok {
				w.Time = t
				markUsed(used, words, i, n)
				i += n - 1
				continue
			}
		}
	}
	var rest []string
	for i, word := range words {
		if !used[i] {
			rest = append(rest, word)
		}
	}
	w.Title = capitalise(strings.Trim(strings.Join(rest, " "), " ,.-"))
	if w.Title == "" {
		w.Title = capitalise(strings.TrimSpace(text))
	}
	return w
}

// markUsed marks n words from i, and a linking word right before them ("on fri", "at 5pm").
func markUsed(used []bool, words []string, i, n int) {
	for k := i; k < i+n; k++ {
		used[k] = true
	}
	if i > 0 && !used[i-1] {
		switch strings.ToLower(words[i-1]) {
		case "on", "at", "by", "@":
			used[i-1] = true
		}
	}
}

var (
	reISO     = regexp.MustCompile(`^(\d{4})-(\d{1,2})-(\d{1,2})$`)
	reDM      = regexp.MustCompile(`^(\d{1,2})[/.-](\d{1,2})(?:[/.-](\d{2,4}))?$`)
	reDay     = regexp.MustCompile(`^(\d{1,2})(?:st|nd|rd|th)?$`)
	reClock   = regexp.MustCompile(`^(\d{1,2})(?::(\d{2}))?\s*(am|pm)?$`)
	reHHMM    = regexp.MustCompile(`^(\d{1,2}):(\d{2})$`)
	monthName = map[string]time.Month{}
	dayName   = map[string]time.Weekday{}
)

func init() {
	for m := time.January; m <= time.December; m++ {
		full := strings.ToLower(m.String())
		monthName[full], monthName[full[:3]] = m, m
	}
	monthName["sept"] = time.September
	for d := time.Sunday; d <= time.Saturday; d++ {
		full := strings.ToLower(d.String())
		dayName[full], dayName[full[:3]] = d, d
	}
	for k, v := range map[string]time.Weekday{"tues": time.Tuesday, "tue": time.Tuesday, "weds": time.Wednesday, "thur": time.Thursday, "thurs": time.Thursday} {
		dayName[k] = v
	}
}

// dateAt reads a date starting at words[i]; n = words it used.
func dateAt(words []string, i int, base time.Time) (time.Time, int, bool) {
	w := strings.ToLower(strings.Trim(words[i], ",."))
	switch w {
	case "today", "tod", "tonight":
		return base, 1, true
	case "tomorrow", "tmrw", "tmr", "tom":
		return base.AddDate(0, 0, 1), 1, true
	case "next":
		if i+1 < len(words) {
			if wd, ok := dayName[strings.ToLower(strings.Trim(words[i+1], ",."))]; ok {
				return nextWeekday(base.AddDate(0, 0, 1), wd), 2, true
			}
		}
	}
	if wd, ok := dayName[w]; ok {
		return nextWeekday(base, wd), 1, true
	}
	if m := reISO.FindStringSubmatch(w); m != nil {
		if d, ok := mkDate(atoi(m[1]), atoi(m[2]), atoi(m[3]), base); ok {
			return d, 1, true
		}
	}
	if m := reDM.FindStringSubmatch(w); m != nil {
		y := 0
		if m[3] != "" {
			y = atoi(m[3])
			if y < 100 {
				y += 2000
			}
		}
		if d, ok := dayMonth(atoi(m[1]), atoi(m[2]), y, base); ok {
			return d, 1, true
		}
	}
	// "14 nov [2026]" / "nov 14 [2026]"
	if i+1 < len(words) {
		a, b := w, strings.ToLower(strings.Trim(words[i+1], ",."))
		day, mon, ok := 0, time.Month(0), false
		if dm := reDay.FindStringSubmatch(a); dm != nil {
			if mm, isM := monthName[b]; isM {
				day, mon, ok = atoi(dm[1]), mm, true
			}
		} else if mm, isM := monthName[a]; isM {
			if dm := reDay.FindStringSubmatch(b); dm != nil {
				day, mon, ok = atoi(dm[1]), mm, true
			}
		}
		if ok {
			n, y := 2, 0
			if i+2 < len(words) {
				if yy, err := strconv.Atoi(strings.Trim(words[i+2], ",.")); err == nil && yy >= 2000 && yy < 2100 {
					n, y = 3, yy
				}
			}
			if d, good := dayMonth(day, int(mon), y, base); good {
				return d, n, true
			}
		}
	}
	return time.Time{}, 0, false
}

// timeAt reads "5pm", "5 pm", "5:30pm", "17:00", "noon".
func timeAt(words []string, i int) (string, int, bool) {
	w := strings.ToLower(strings.Trim(words[i], ",."))
	if w == "noon" {
		return "12:00", 1, true
	}
	if w == "midnight" {
		return "00:00", 1, true
	}
	if m := reHHMM.FindStringSubmatch(w); m != nil && atoi(m[1]) < 24 && atoi(m[2]) < 60 {
		return fmt.Sprintf("%02d:%s", atoi(m[1]), m[2]), 1, true
	}
	n := 1
	if i+1 < len(words) {
		if next := strings.ToLower(strings.Trim(words[i+1], ",.")); (next == "am" || next == "pm") && reDay.MatchString(w) {
			w, n = w+next, 2
		}
	}
	m := reClock.FindStringSubmatch(w)
	if m == nil || m[3] == "" { // a bare number is not a time
		return "", 0, false
	}
	h, min := atoi(m[1]), 0
	if m[2] != "" {
		min = atoi(m[2])
	}
	if h < 1 || h > 12 || min > 59 {
		return "", 0, false
	}
	if m[3] == "pm" && h != 12 {
		h += 12
	}
	if m[3] == "am" && h == 12 {
		h = 0
	}
	return fmt.Sprintf("%02d:%02d", h, min), n, true
}

// dayMonth builds a date; without a year it is the next such day on or after base.
func dayMonth(day, month, year int, base time.Time) (time.Time, bool) {
	if year != 0 {
		return mkDate(year, month, day, base)
	}
	d, ok := mkDate(base.Year(), month, day, base)
	if ok && d.Before(base) {
		d, ok = mkDate(base.Year()+1, month, day, base)
	}
	return d, ok
}

func mkDate(y, m, d int, base time.Time) (time.Time, bool) {
	if m < 1 || m > 12 || d < 1 || d > 31 {
		return time.Time{}, false
	}
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, base.Location())
	return t, t.Day() == d // rejects 31 Feb
}

// nextWeekday is the first wd on or after from.
func nextWeekday(from time.Time, wd time.Weekday) time.Time {
	return from.AddDate(0, 0, (int(wd)-int(from.Weekday())+7)%7)
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func capitalise(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}
