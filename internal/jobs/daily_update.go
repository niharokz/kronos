package jobs

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"gitlab.com/niharokz/kronos/internal/dates"
	"gitlab.com/niharokz/kronos/internal/job"
	"gitlab.com/niharokz/kronos/internal/store"
)

// daily_update writes the agenda (paths.daily) with three sections —
// Overdue, Due Today, Upcoming (next upcoming_days, default 7) — from
// events, subscriptions and birthdays. If a source file can't be read the
// agenda is still written, then the job fails so you get an alert.

func init() { job.Register("daily_update", dailyUpdate) }

func dailyUpdate(c *job.Ctx) error {
	var problems []string
	overdue, today, upcoming := buildAgenda(c, &problems)

	var b strings.Builder
	for i, sec := range []struct {
		heading string
		items   []string
	}{{"Overdue", overdue}, {"Due Today", today}, {"Upcoming", upcoming}} {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("# " + sec.heading + "\n")
		sort.Strings(sec.items)
		for _, item := range sec.items {
			b.WriteString(item + "\n")
		}
	}

	path := c.Path("daily") // kronos.yml: paths.daily
	if err := c.MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	if err := c.WriteFile(path, []byte(b.String())); err != nil {
		return err
	}
	c.Log.OK("Generated %s (overdue=%d, today=%d, upcoming=%d)", filepath.Base(path), len(overdue), len(today), len(upcoming))

	if len(problems) > 0 {
		return fmt.Errorf("Agenda incomplete – unreadable: %s", strings.Join(problems, ", "))
	}
	return nil
}

// readList returns the mappings under key; a broken file is recorded in
// problems and treated as empty.
func readList(c *job.Ctx, path, key string, problems *[]string) []*yaml.Node {
	data, err := store.LoadMapping(path, false)
	if err != nil {
		c.Log.Warn("%v", err)
		*problems = append(*problems, filepath.Base(path))
		return nil
	}
	v := store.Get(data, key)
	if !store.Truthy(v) || v.Kind != yaml.SequenceNode {
		return nil
	}
	var out []*yaml.Node
	for _, item := range v.Content {
		if item = store.Resolve(item); item.Kind == yaml.MappingNode {
			out = append(out, item)
		}
	}
	return out
}

func buildAgenda(c *job.Ctx, problems *[]string) (overdue, dueToday, upcoming []string) {
	today := c.Today
	horizon := today.AddDate(0, 0, c.Int("upcoming_days", 7)) // kronos.yml: jobs.daily_update.options.upcoming_days

	for _, e := range readList(c, c.Path("events"), "events", problems) { // kronos.yml: paths.events
		status := strings.ToLower(pyGet(e, "status", ""))
		when, ok := store.DateOf(e, "date")
		if (status != "open" && status != "pending") || !ok {
			continue
		}
		title := firstTruthy(e, "Unknown", "title", "id")
		switch {
		case status == "pending" || when.Before(today):
			overdue = append(overdue, fmt.Sprintf("EVENT: %s (%s)", title, dates.Format(when)))
		case when.Equal(today):
			dueToday = append(dueToday, "EVENT: "+title)
		case !when.After(horizon):
			upcoming = append(upcoming, fmt.Sprintf("EVENT: %s (%s)", title, dates.Format(when)))
		}
	}

	for _, s := range readList(c, c.Path("subscriptions"), "subscriptions", problems) { // kronos.yml: paths.subscriptions
		due, ok := store.DateOf(s, "next_due_date")
		if strings.ToLower(pyGet(s, "status", "")) != "active" || !ok {
			continue
		}
		name := pyGet(s, "name", "Unknown")
		switch {
		case due.Before(today):
			overdue = append(overdue, fmt.Sprintf("SUBSCRIPTION- %s (%s)", name, dates.Format(due)))
		// Due-today subscriptions appear via their auto-created EVENT (subscription_update).
		case due.After(today) && !due.After(horizon):
			upcoming = append(upcoming, fmt.Sprintf("SUBSCRIPTION- %s (%s)", name, dates.Format(due)))
		}
	}

	for _, p := range readList(c, c.Path("birthdays"), "people", problems) { // kronos.yml: paths.birthdays
		parts := strings.Split(pyGet(p, "birthday", ""), "-")
		if len(parts) != 3 {
			continue
		}
		month, err1 := strconv.Atoi(strings.TrimSpace(parts[1]))
		day, err2 := strconv.Atoi(strings.TrimSpace(parts[2]))
		if err1 != nil || err2 != nil {
			continue
		}
		when := nextBirthday(month, day, today)
		name := pyGet(p, "name", "Unknown")
		switch {
		case when.Equal(today):
			dueToday = append(dueToday, "BIRTHDAY- "+name)
		case !when.After(horizon):
			upcoming = append(upcoming, fmt.Sprintf("BIRTHDAY- %s (%s)", name, dates.Format(when)))
		}
	}
	return overdue, dueToday, upcoming
}

// nextBirthday is the next occurrence on or after today. An impossible date
// (29 Feb in a non-leap year, or bad data) falls back to 28 Feb, as before.
func nextBirthday(month, day int, today time.Time) time.Time {
	for _, year := range []int{today.Year(), today.Year() + 1} {
		candidate := dates.New(year, 2, 28)
		if month >= 1 && month <= 12 && day >= 1 && day <= dates.DaysIn(year, time.Month(month)) {
			candidate = dates.New(year, time.Month(month), day)
		}
		if !candidate.Before(today) {
			return candidate
		}
	}
	return dates.New(today.Year()+1, 2, 28)
}
