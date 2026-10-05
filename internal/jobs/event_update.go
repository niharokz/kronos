package jobs

import (
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"gitlab.com/niharokz/kronos/internal/dates"
	"gitlab.com/niharokz/kronos/internal/job"
	"gitlab.com/niharokz/kronos/internal/store"
)

// event_update maintains event.md: overdue one-time events become pending
// today, overdue recurring events move to their next date, closed recurring
// events reopen at their next date, very old closed one-time events are
// purged, and everything is sorted.
//
// Options: purge_after_days (default 365).

func init() { job.Register("event_update", eventUpdate) }

var (
	priorityOrder   = map[string]int{"high": 0, "medium": 1, "low": 2}
	requiredFields  = []string{"id", "title", "date", "status", "frequency", "priority"}
	validStatus     = map[string]bool{"open": true, "closed": true, "pending": true}
	validFrequency  = map[string]bool{"none": true, "daily": true, "weekly": true, "monthly": true, "quarterly": true, "yearly": true}
	validPriority   = map[string]bool{"high": true, "medium": true, "low": true}
	legacyFrequency = map[string]string{"once": "none"}
	legacyPriority  = map[string]string{"normal": "medium"}
)

func eventUpdate(c *job.Ctx) error {
	path := c.Path("events") // kronos.yml: paths.events
	events, err := store.LoadRecords(path, "events", true)
	if err != nil {
		return err
	}
	c.Log.Info("Loaded %d events", len(events))

	normalizeLegacy(c, events)
	validateEvents(c, events)
	moveOverdueOneTime(c, events)
	advanceOverdueRecurring(c, events)
	reopenClosedRecurring(c, events)
	events = purgeOldClosed(c, events)
	sortEvents(events)

	data, err := store.RecordsYAML(filepath.Base(path), "events", events, c.Now)
	if err != nil {
		return err
	}
	if err := c.WriteFile(path, data); err != nil {
		return err
	}
	c.Log.OK("Saved %d events", len(events))
	return nil
}

// normalizeLegacy rewrites the old values Omnimo's shared schema replaced:
// frequency once → none, priority normal → medium.
func normalizeLegacy(c *job.Ctx, events []*yaml.Node) {
	count := 0
	for _, e := range events {
		for key, table := range map[string]map[string]string{"frequency": legacyFrequency, "priority": legacyPriority} {
			n := store.Get(e, key)
			if n == nil || n.Kind != yaml.ScalarNode || n.ShortTag() != "!!str" {
				continue
			}
			if repl, ok := table[strings.ToLower(strings.TrimSpace(n.Value))]; ok {
				store.Set(e, key, store.Str(repl))
				count++
			}
		}
	}
	if count > 0 {
		c.Log.Info("Normalised %d legacy value(s) (once → none, normal → medium)", count)
	}
}

// validateEvents warns about missing or invalid fields; it never changes data.
func validateEvents(c *job.Ctx, events []*yaml.Node) {
	issues := 0
	for _, e := range events {
		prefix := "[" + label(e) + "]"
		for _, f := range requiredFields {
			if store.IsNull(store.Get(e, f)) {
				c.Log.Warn("%s Missing field '%s'", prefix, f)
				issues++
			}
		}
		for _, check := range []struct {
			field string
			valid map[string]bool
		}{{"status", validStatus}, {"frequency", validFrequency}, {"priority", validPriority}} {
			n := store.Get(e, check.field)
			if store.IsNull(n) {
				continue
			}
			if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!str" || !check.valid[n.Value] {
				c.Log.Warn("%s Invalid %s '%s'", prefix, check.field, store.PyStr(n))
				issues++
			}
		}
		for _, f := range []string{"date", "last_done"} {
			n := store.Get(e, f)
			if store.IsNull(n) {
				continue
			}
			if _, ok := store.DateOf(e, f); !ok {
				c.Log.Warn("%s Unparseable %s '%s'", prefix, f, store.PyStr(n))
				issues++
			}
		}
	}
	if issues > 0 {
		c.Log.Warn("Validation found %d issue(s) – continuing", issues)
	}
}

// moveOverdueOneTime: open/pending one-time events in the past → today, pending.
func moveOverdueOneTime(c *job.Ctx, events []*yaml.Node) {
	count := 0
	for _, e := range events {
		current, ok := store.DateOf(e, "date")
		if isActive(e) && isOneTime(e) && ok && current.Before(c.Today) {
			store.Set(e, "date", store.Date(c.Today))
			store.Set(e, "status", store.Str("pending"))
			count++
		}
	}
	c.Log.Info("Moved %d overdue one-time event(s) to today (pending)", count)
}

// advanceOverdueRecurring: open/pending recurring events in the past → next
// occurrence on or after today.
func advanceOverdueRecurring(c *job.Ctx, events []*yaml.Node) {
	count := 0
	for _, e := range events {
		current, ok := store.DateOf(e, "date")
		if !isActive(e) || isOneTime(e) || !ok || !current.Before(c.Today) {
			continue
		}
		rule := store.Field(e, "frequency")
		anchor := ruleAnchor(e, rule, current)
		next, ok := dates.RollForward(current, rule, c.Today, anchor)
		if !ok {
			c.Log.Warn("[%s] Unknown frequency '%s' – skipped", label(e), pyGet(e, "frequency", ""))
			continue
		}
		store.Set(e, "date", store.Date(next))
		rememberAnchor(e, rule, anchor)
		count++
	}
	c.Log.Info("Advanced %d recurring event(s)", count)
}

// reopenClosedRecurring: closed recurring events → next occurrence (always
// at least one step, so closing early works), open, last_done set.
func reopenClosedRecurring(c *job.Ctx, events []*yaml.Node) {
	count := 0
	for _, e := range events {
		current, ok := store.DateOf(e, "date")
		if !store.Equals(e, "status", "closed") || isOneTime(e) || !ok {
			continue
		}
		rule := store.Field(e, "frequency")
		anchor := ruleAnchor(e, rule, current)
		next, ok := dates.Advance(current, rule, anchor)
		if ok {
			next, ok = dates.RollForward(next, rule, c.Today, anchor)
		}
		if !ok {
			c.Log.Warn("[%s] Unknown frequency '%s' – not reopened", label(e), pyGet(e, "frequency", ""))
			continue
		}
		store.Set(e, "last_done", store.Date(current))
		store.Set(e, "date", store.Date(next))
		store.Set(e, "status", store.Str("open"))
		rememberAnchor(e, rule, anchor)
		count++
	}
	c.Log.Info("Reopened %d recurring event(s)", count)
}

// purgeOldClosed drops closed one-time events older than purge_after_days
// (a safety net: event_archive normally moves them out at 30 days).
func purgeOldClosed(c *job.Ctx, events []*yaml.Node) []*yaml.Node {
	cutoff := c.Today.AddDate(0, 0, -c.Int("purge_after_days", 365)) // kronos.yml: jobs.event_update.options.purge_after_days
	kept := events[:0:0]
	for _, e := range events {
		current, ok := store.DateOf(e, "date")
		if store.Equals(e, "status", "closed") && isOneTime(e) && ok && current.Before(cutoff) {
			continue
		}
		kept = append(kept, e)
	}
	c.Log.Info("Purged %d old closed one-time event(s)", len(events)-len(kept))
	return kept
}

// sortEvents: active first, then by date, then by priority (stable).
func sortEvents(events []*yaml.Node) {
	type key struct {
		inactive int
		date     int64
		prio     int
	}
	keyOf := func(e *yaml.Node) key {
		k := key{inactive: 1, prio: 99}
		if isActive(e) {
			k.inactive = 0
		}
		if d, ok := store.DateOf(e, "date"); ok {
			k.date = d.Unix()
		} else {
			k.date = dates.Max.Unix()
		}
		if n := store.Get(e, "priority"); n != nil && n.ShortTag() == "!!str" {
			if p, ok := priorityOrder[n.Value]; ok {
				k.prio = p
			}
		}
		return k
	}
	sort.SliceStable(events, func(i, j int) bool {
		a, b := keyOf(events[i]), keyOf(events[j])
		if a.inactive != b.inactive {
			return a.inactive < b.inactive
		}
		if a.date != b.date {
			return a.date < b.date
		}
		return a.prio < b.prio
	})
}
