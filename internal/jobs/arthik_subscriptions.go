package jobs

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"gitlab.com/niharokz/kronos/internal/dates"
	"gitlab.com/niharokz/kronos/internal/job"
	"gitlab.com/niharokz/kronos/internal/store"
)

// arthik_subscriptions hands subscriptions that fall due to Arthik as drafts to
// review: it writes <finance>/inbox/kronos.md, a file only Kronos writes. A
// subscription is due on the day subscription_update creates its
// "sub-<name>-<date>" event (or its next_due_date is today). Draft ids are the
// event ids, so a rerun never adds one twice, and Arthik never imports one twice.
//
// Only days since the last run count (kronos.md remembers last_run), so old due
// events — from before this job existed, or for bills you already handled — are
// never drafted. The first run looks at today only; after downtime it catches up
// at most max_catch_up_days.
//
// options: max_catch_up_days (3) — days caught up after downtime
//          keep_days (60)        — drafts older than this are dropped from kronos.md

func init() { job.Register("arthik_subscriptions", arthikSubscriptions) }

const arthikDraftsKey = "drafts"

func arthikSubscriptions(c *job.Ctx) error {
	out := filepath.Join(c.Path("finance_inbox"), "kronos.md")                     // kronos.yml: paths.finance_inbox
	subs, err := store.LoadRecords(c.Path("subscriptions"), "subscriptions", true) // kronos.yml: paths.subscriptions
	if err != nil {
		return err
	}
	events, err := store.LoadRecords(c.Path("events"), "events", false) // kronos.yml: paths.events
	if err != nil {
		c.Log.Warn("%v – using due dates only", err)
		events = nil
	}
	root, err := store.LoadMapping(out, false)
	if err != nil {
		return err
	}
	drafts, err := store.RecordList(store.Get(root, arthikDraftsKey), filepath.Base(out), arthikDraftsKey)
	if err != nil {
		return err
	}

	// Window: the days after the last run up to today (first run: today only).
	from := c.Today
	if last, ok := store.DateOf(root, "last_run"); ok && last.Before(c.Today) {
		from = last.AddDate(0, 0, 1)
	}
	if earliest := c.Today.AddDate(0, 0, -c.Int("max_catch_up_days", 3)); from.Before(earliest) {
		from = earliest
	}
	inWindow := func(d time.Time) bool { return !d.Before(from) && !d.After(c.Today) }

	due := map[string]*yaml.Node{} // draft id → subscription
	var order []string
	add := func(id string, sub *yaml.Node) {
		if _, ok := due[id]; !ok && store.Equals(sub, "status", "active") {
			due[id] = sub
			order = append(order, id)
		}
	}
	for _, e := range events {
		id := store.Field(e, "id")
		day, ok := store.DateOf(e, "date")
		if !strings.HasPrefix(id, "sub-") || !ok || !inWindow(day) {
			continue
		}
		for _, s := range subs {
			if id == subEventID(s, day) {
				add(id, s)
			}
		}
	}
	for _, s := range subs {
		if day, ok := store.DateOf(s, "next_due_date"); ok && day.Equal(c.Today) {
			add(subEventID(s, day), s)
		}
	}

	// Keep recent drafts, add new ones.
	oldestKept := c.Today.AddDate(0, 0, -c.Int("keep_days", 60))
	have := map[string]bool{}
	kept := []*yaml.Node{}
	pruned := 0
	for _, d := range drafts {
		if day, ok := store.DateOf(d, "date"); ok && day.Before(oldestKept) {
			pruned++
			continue
		}
		have[store.Field(d, "id")] = true
		kept = append(kept, d)
	}
	added := 0
	for _, id := range order {
		if have[id] {
			continue
		}
		kept = append(kept, subDraft(id, due[id]))
		added++
		c.Log.Info("Draft for Arthik: %s", pyGet(due[id], "name", "?"))
	}
	if last, _ := store.DateOf(root, "last_run"); added == 0 && pruned == 0 && last.Equal(c.Today) {
		c.Log.Skip("No new subscription due")
		return nil
	}

	doc := store.NewMapping()
	store.Set(doc, "last_run", store.Date(c.Today))
	store.Set(doc, arthikDraftsKey, store.Seq(kept...))
	body, err := store.Encode(doc)
	if err != nil {
		return err
	}
	if err := c.MkdirAll(filepath.Dir(out)); err != nil {
		return err
	}
	if err := c.WriteFile(out, append([]byte("updated: "+c.Now.Format("2006-01-02 15:04:05")+"\n\n"), body...)); err != nil {
		return err
	}
	c.Log.OK("Added %d draft(s), dropped %d old (checked %s – %s)", added, pruned, dates.Format(from), dates.Format(c.Today))
	return nil
}

// subEventID is the id subscription_update gives a subscription's due event.
func subEventID(sub *yaml.Node, day time.Time) string {
	return fmt.Sprintf("sub-%s-%s", slugify(pyGet(sub, "name", "Unknown")), dates.Format(day))
}

// subDraft is one kronos.md entry, in the layout Arthik reads.
func subDraft(id string, sub *yaml.Node) *yaml.Node {
	d := store.NewMapping()
	day := id[len(id)-len(dates.Layout):]
	store.Set(d, "id", store.Str(id))
	store.Set(d, "source", store.Str("subscription"))
	store.Set(d, "date", store.Quoted(day))
	store.Set(d, "title", store.Str(pyGet(sub, "name", "Subscription")))
	store.Set(d, "type", store.Str("expense"))
	for _, k := range []string{"amount", "account", "category"} {
		if v := store.Get(sub, k); v != nil && !store.IsNull(v) {
			store.Set(d, k, store.Copy(v))
		}
	}
	if cycle := store.Field(sub, "cycle"); cycle != "" {
		store.Set(d, "notes", store.Str("Subscription · "+cycle))
	}
	return d
}
