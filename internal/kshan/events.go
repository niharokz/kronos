package kshan

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"gitlab.com/niharokz/kronos/internal/dates"
	"gitlab.com/niharokz/kronos/internal/job"
	"gitlab.com/niharokz/kronos/internal/store"
)

// kshan_events imports the phone's event drafts into event.md (replaces RahiOS's planned rahi_events).
//
// Input: paths.kshan_events (Kshan: `drafts: [{text, created}]`, text like "dentist fri 5pm"),
// then paths.kshan_events_old (RahiOS: `drafts: [{id, title, date, time, created}]`), read only.
// Each new draft becomes a one-time open event; the time, if any, is appended to the title.
// Id = kshan-<created YYYYMMDD-HHMMSS> (RahiOS drafts keep their id), so a draft is imported once:
// it is skipped while its id is in event.md or event.md.bck, and drafts older than max_age_days are ignored.
// Kronos never writes the drafts files.
//
// Options: max_age_days (30).

func init() { job.Register("kshan_events", kshanEvents) }

// draft is one event draft, whichever app wrote it.
type draft struct {
	ID      string
	Title   string
	Date    time.Time
	Time    string
	Created time.Time
	Source  string
}

func kshanEvents(c *job.Ctx) error {
	var drafts []draft
	for _, path := range []string{c.Path("kshan_events"), c.OptionalPath("kshan_events_old")} { // kronos.yml: paths.kshan_events(_old)
		if path == "" {
			continue
		}
		got, err := loadDrafts(path, c.Today)
		if err != nil {
			return err // unreadable drafts: touch nothing, FAIL so it gets fixed
		}
		drafts = append(drafts, got...)
	}
	cutoff := c.Today.AddDate(0, 0, -c.Int("max_age_days", 30)) // kronos.yml: jobs.kshan_events.options.max_age_days
	var fresh []draft
	for _, d := range drafts {
		if d.Created.IsZero() || !d.Created.Before(cutoff) {
			fresh = append(fresh, d)
		}
	}
	if len(fresh) == 0 {
		c.Log.Skip("No drafts")
		return nil
	}

	eventPath := c.Path("events") // kronos.yml: paths.events
	events, err := store.LoadRecords(eventPath, "events", true)
	if err != nil {
		return err // never treat a broken event.md as empty
	}
	known := map[string]bool{}
	for _, e := range events {
		known[store.Field(e, "id")] = true
	}
	if old, err := store.LoadRecords(c.Path("event_backup"), "events", false); err == nil { // kronos.yml: paths.event_backup
		for _, e := range old {
			known[store.Field(e, "id")] = true
		}
	} else {
		c.Log.Warn("%v – archived events not checked", err)
	}

	added := 0
	for _, d := range fresh {
		if known[d.ID] {
			continue
		}
		known[d.ID] = true
		events = append(events, eventFor(d))
		c.Log.Info("Imported %s: %s on %s", d.ID, d.Title, dates.Format(d.Date))
		added++
	}
	if added == 0 {
		c.Log.Skip("All %d draft(s) already imported", len(fresh))
		return nil
	}
	data, err := store.RecordsYAML(filepath.Base(eventPath), "events", events, c.Now)
	if err != nil {
		return err
	}
	if err := c.WriteFile(eventPath, data); err != nil {
		return err
	}
	c.Log.OK("Imported %d event(s)", added)
	return nil
}

// eventFor is the event.md record for a draft.
func eventFor(d draft) *yaml.Node {
	title := d.Title
	if d.Time != "" {
		title += " " + d.Time
	}
	e := store.NewMapping()
	store.Set(e, "id", store.Str(d.ID))
	store.Set(e, "title", store.Str(title))
	store.Set(e, "date", store.Date(d.Date))
	store.Set(e, "status", store.Str("open"))
	store.Set(e, "frequency", store.Str("none"))
	store.Set(e, "priority", store.Str("medium"))
	return e
}

// loadDrafts reads a drafts file; missing = none. Kshan items are text (+ created), RahiOS items
// have id/title/date. A plain string item is a Kshan text with no created stamp.
func loadDrafts(path string, today time.Time) ([]draft, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		Drafts []yaml.Node `yaml:"drafts"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("Invalid YAML in %s: %v", filepath.Base(path), err)
	}
	var out []draft
	for i := range doc.Drafts {
		n := &doc.Drafts[i]
		if n.Kind == yaml.ScalarNode {
			out = append(out, kshanDraft(n.Value, "", today, path))
			continue
		}
		if n.Kind != yaml.MappingNode {
			continue
		}
		if id := store.Field(n, "id"); id != "" && store.Field(n, "title") != "" { // RahiOS
			d := draft{ID: id, Title: store.Field(n, "title"), Time: store.Field(n, "time"), Source: path}
			if t, ok := store.DateOf(n, "date"); ok {
				d.Date = t
			} else {
				d.Date = today
			}
			d.Created, _ = parseStamp(store.Field(n, "created"))
			out = append(out, d)
			continue
		}
		if text := strings.TrimSpace(store.Field(n, "text")); text != "" { // Kshan
			out = append(out, kshanDraft(text, store.Field(n, "created"), today, path))
		}
	}
	return out, nil
}

func kshanDraft(text, created string, today time.Time, path string) draft {
	d := draft{Source: path}
	base := today
	if t, ok := parseStamp(created); ok {
		d.Created, base = t, t
		d.ID = "kshan-" + t.Format("20060102-150405")
	} else {
		sum := sha1.Sum([]byte(text))
		d.ID = "kshan-" + hex.EncodeToString(sum[:])[:10]
	}
	w := ParseWhen(text, base)
	d.Title, d.Date, d.Time = w.Title, w.Date, w.Time
	return d
}

// parseStamp reads "2026-11-12 19:30:00" (or with T, or without seconds) in local time.
func parseStamp(s string) (time.Time, bool) {
	s = strings.TrimSpace(strings.Replace(s, "T", " ", 1))
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
