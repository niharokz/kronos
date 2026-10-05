package jobs

import (
	"fmt"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"gitlab.com/niharokz/kronos/internal/dates"
	"gitlab.com/niharokz/kronos/internal/job"
	"gitlab.com/niharokz/kronos/internal/store"
)

// subscription_update maintains subscription.md: overdue due dates roll
// forward by their cycle, anything due today gets a one-time pending event
// in event.md (id sub-<name>-<date>, so reruns never duplicate it), and the
// list is sorted.
//
// Rolling forward happens before the due-today check, so a day missed while
// the host was off still creates the event when the catch-up lands on today.

func init() { job.Register("subscription_update", subscriptionUpdate) }

func subscriptionUpdate(c *job.Ctx) error {
	subPath, eventPath := c.Path("subscriptions"), c.Path("events") // kronos.yml: paths.subscriptions, paths.events

	subs, err := loadSubscriptions(c, subPath)
	if err != nil {
		return err
	}
	if err := updateSubscriptions(c, subs, eventPath); err != nil {
		return err
	}
	sortSubscriptions(subs)

	data, err := store.RecordsYAML(filepath.Base(subPath), "subscriptions", subs, c.Now)
	if err != nil {
		return err
	}
	if err := c.WriteFile(subPath, data); err != nil {
		return err
	}
	c.Log.OK("Saved %d subscriptions", len(subs))
	return nil
}

// loadSubscriptions returns the list, also accepting the legacy
// active:/cancelled: layout (always written back as subscriptions:).
func loadSubscriptions(c *job.Ctx, path string) ([]*yaml.Node, error) {
	name := filepath.Base(path)
	data, err := store.LoadMapping(path, true)
	if err != nil {
		return nil, err
	}

	var subs []*yaml.Node
	switch {
	case store.Has(data, "subscriptions"):
		subs, err = store.RecordList(store.Get(data, "subscriptions"), name, "subscriptions")
	case store.Has(data, "active") || store.Has(data, "cancelled"):
		var active, cancelled []*yaml.Node
		if active, err = store.RecordList(store.Get(data, "active"), name, "subscriptions"); err == nil {
			cancelled, err = store.RecordList(store.Get(data, "cancelled"), name, "subscriptions")
		}
		subs = append(active, cancelled...)
		c.Log.Info("Legacy active/cancelled layout – will be rewritten as 'subscriptions'")
	default:
		err = fmt.Errorf("%s: missing 'subscriptions' list", name)
	}
	if err != nil {
		return nil, err
	}
	c.Log.Info("Loaded %d subscriptions", len(subs))
	return subs, nil
}

func updateSubscriptions(c *job.Ctx, subs []*yaml.Node, eventPath string) error {
	events, err := store.LoadRecords(eventPath, "events", true)
	if err != nil {
		c.Log.Warn("%v – skipping due-event creation", err)
		events = nil
	}
	eventsUsable := err == nil

	advanced, created := 0, 0
	for _, sub := range subs {
		if !store.Equals(sub, "status", "active") {
			continue
		}
		due, ok := store.DateOf(sub, "next_due_date")
		if !ok {
			continue
		}

		if due.Before(c.Today) {
			rule := store.Field(sub, "cycle")
			anchor := ruleAnchor(sub, rule, due)
			next, ok := dates.RollForward(due, rule, c.Today, anchor)
			if !ok {
				c.Log.Warn("Unknown cycle '%s' on '%s' – skipped", pyGet(sub, "cycle", "None"), pyGet(sub, "name", "?"))
				continue
			}
			store.Set(sub, "next_due_date", store.Date(next))
			rememberAnchor(sub, rule, anchor)
			due = next
			advanced++
		}

		if due.Equal(c.Today) && eventsUsable {
			var added bool
			events, added = createDueEvent(c, sub, events)
			if added {
				created++
			}
		}
	}

	if created > 0 {
		data, err := store.RecordsYAML(filepath.Base(eventPath), "events", events, c.Now)
		if err != nil {
			return err
		}
		if err := c.WriteFile(eventPath, data); err != nil {
			return err
		}
	}
	c.Log.Info("Advanced %d, created %d due event(s)", advanced, created)
	return nil
}

// createDueEvent appends a pending one-time event for a subscription due
// today, unless its deterministic id already exists.
func createDueEvent(c *job.Ctx, sub *yaml.Node, events []*yaml.Node) ([]*yaml.Node, bool) {
	name := pyGet(sub, "name", "Unknown")
	id := fmt.Sprintf("sub-%s-%s", slugify(name), dates.Format(c.Today))
	for _, e := range events {
		if store.Equals(e, "id", id) {
			return events, false
		}
	}
	e := store.NewMapping()
	store.Set(e, "id", store.Str(id))
	store.Set(e, "title", store.Str("Subscription due: "+name))
	store.Set(e, "date", store.Date(c.Today))
	store.Set(e, "status", store.Str("pending"))
	store.Set(e, "frequency", store.Str("none"))
	store.Set(e, "priority", store.Str("medium"))
	c.Log.Info("Created event for due subscription '%s'", name)
	return append(events, e), true
}

// sortSubscriptions: active first, then by next due date (stable).
func sortSubscriptions(subs []*yaml.Node) {
	keyOf := func(s *yaml.Node) (int, int64) {
		inactive := 1
		if store.Equals(s, "status", "active") {
			inactive = 0
		}
		if d, ok := store.DateOf(s, "next_due_date"); ok {
			return inactive, d.Unix()
		}
		return inactive, dates.Max.Unix()
	}
	sort.SliceStable(subs, func(i, j int) bool {
		ai, ad := keyOf(subs[i])
		bi, bd := keyOf(subs[j])
		if ai != bi {
			return ai < bi
		}
		return ad < bd
	})
}
