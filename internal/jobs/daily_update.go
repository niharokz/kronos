package jobs

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"gitlab.com/niharokz/kronos/internal/dates"
	"gitlab.com/niharokz/kronos/internal/job"
	"gitlab.com/niharokz/kronos/internal/store"
)

// daily_update writes the agenda (paths.daily) with three sections —
// Overdue, Due Today, Upcoming (next upcoming_days, default 7) — from
// events, subscriptions and calendar.md (from calendar_sync). If a source
// file can't be read the agenda is still written, then the job fails so you
// get an alert.

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

	for _, e := range readList(c, c.Path("calendar"), "events", problems) { // kronos.yml: paths.calendar
		first, ok := store.DateOf(e, "date")
		if !ok {
			continue
		}
		last, ok := store.DateOf(e, "end") // multi-day: shown every day it lasts
		if !ok || last.Before(first) {
			last = first
		}
		item := calendarPrefix(pyGet(e, "category", "")) + calendarTitle(e)
		if t := pyGet(e, "time", ""); t != "" {
			item += " " + t
		}
		switch {
		case last.Before(today):
			// past entries (calendar_sync keeps a few days back) are not agenda items
		case !first.After(today):
			dueToday = append(dueToday, item)
		case !first.After(horizon):
			upcoming = append(upcoming, fmt.Sprintf("%s (%s)", item, dates.Format(first)))
		}
	}
	return overdue, dueToday, upcoming
}

// calendarPrefix marks an agenda line by its calendar.md category.
func calendarPrefix(category string) string {
	switch category {
	case "birthday", "anniversary":
		return "BIRTHDAY- "
	case "holiday", "festival":
		return "HOLIDAY- "
	}
	return "CALENDAR- "
}

// calendarTitle drops Google's "'s birthday" suffix, so birthdays read "BIRTHDAY- Asha".
func calendarTitle(e *yaml.Node) string {
	title := pyGet(e, "title", "Unknown")
	if pyGet(e, "category", "") == "birthday" {
		for _, suffix := range []string{"'s birthday", "’s birthday"} {
			if strings.HasSuffix(strings.ToLower(title), suffix) && len(title) > len(suffix) {
				return title[:len(title)-len(suffix)]
			}
		}
	}
	return title
}
