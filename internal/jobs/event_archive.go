package jobs

import (
	"path/filepath"

	"gopkg.in/yaml.v3"

	"gitlab.com/niharokz/kronos/internal/job"
	"gitlab.com/niharokz/kronos/internal/store"
)

// event_archive moves closed one-time events older than archive_after_days
// (default 30) from event.md into the backup file (paths.event_backup).
// Recurring events are never archived — event_update reopens them.
//
// The backup is written first, then event.md, so a crash in between can
// duplicate an event but never lose one; re-runs skip ids already archived.

func init() { job.Register("event_archive", eventArchive) }

func eventArchive(c *job.Ctx) error {
	path, backup := c.Path("events"), c.Path("event_backup")          // kronos.yml: paths.events, paths.event_backup
	cutoff := c.Today.AddDate(0, 0, -c.Int("archive_after_days", 30)) // kronos.yml: jobs.event_archive.options.archive_after_days

	events, err := store.LoadRecords(path, "events", true)
	if err != nil {
		return err
	}

	var toArchive, keep []*yaml.Node
	for _, e := range events {
		current, ok := store.DateOf(e, "date")
		if store.Equals(e, "status", "closed") && isOneTime(e) && ok && current.Before(cutoff) {
			toArchive = append(toArchive, e)
		} else {
			keep = append(keep, e)
		}
	}

	if len(toArchive) == 0 {
		c.Log.Skip("No closed events to archive")
		return nil
	}
	if len(keep) == 0 {
		c.Log.Warn("Archiving would empty %s – skipped", filepath.Base(path))
		return nil
	}

	archived, err := store.LoadRecords(backup, "events", false)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, e := range archived {
		if id := store.Get(e, "id"); store.Truthy(id) {
			known[idKey(id)] = true
		}
	}
	var fresh []*yaml.Node
	for _, e := range toArchive {
		id := store.Get(e, "id")
		if !store.Truthy(id) || !known[idKey(id)] {
			fresh = append(fresh, e)
		}
	}

	backupData, err := store.RecordsYAML(filepath.Base(backup), "events", append(archived, fresh...), c.Now)
	if err != nil {
		return err
	}
	if err := c.WriteFile(backup, backupData); err != nil { // backup first
		return err
	}
	eventData, err := store.RecordsYAML(filepath.Base(path), "events", keep, c.Now)
	if err != nil {
		return err
	}
	if err := c.WriteFile(path, eventData); err != nil { // then the source
		return err
	}

	c.Log.OK("Archived %d event(s) to %s (%d new)", len(toArchive), filepath.Base(backup), len(fresh))
	return nil
}

// idKey compares ids by type and value, like Python's == (1 != "1").
func idKey(n *yaml.Node) string { return n.ShortTag() + ":" + store.PyStr(n) }
