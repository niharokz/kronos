package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gitlab.com/niharokz/kronos/internal/job"
	"gitlab.com/niharokz/kronos/internal/outbox"
	"gitlab.com/niharokz/kronos/internal/pulse"
	"gitlab.com/niharokz/kronos/internal/store"
)

// Shared by pulse_watch and pulse_rollup (logic lives in internal/pulse).
// Kronos never writes to <rahi>/daily/ — only goals.md (once), baseline.md,
// state/, weekly/, monthly/ and inbox/.

// pulseGoals reads goals.md, writing the commented default when it is missing.
func pulseGoals(c *job.Ctx, dir string) (pulse.Goals, error) {
	path := pulse.GoalsFile(dir)
	g, exists, err := pulse.LoadGoals(path)
	if err != nil {
		return g, err
	}
	if !exists {
		if err := c.MkdirAll(dir); err != nil {
			return g, err
		}
		if err := pulseWrite(c, path, []byte(pulse.DefaultGoals)); err != nil {
			return g, err
		}
		c.Log.Info("Created %s with defaults — add your tracked_apps", path)
	}
	return g, nil
}

// pulseDeliver sends one message on its channel and records it in sent.
// In a dry run it prints the message (priority, channel, id) instead.
func pulseDeliver(c *job.Ctx, dir string, m pulse.Message, sent *pulse.Sent, forDay string) error {
	if c.DryRun {
		prio := fmt.Sprintf("p%d", pulse.Priority(m.Type))
		if m.Type == pulse.TypeReport {
			prio = "report" // outside the daily cap
		}
		fmt.Fprintf(c.Out, "~ would send [%s] %-8s %s — %s\n    %s\n", prio, m.Channel, m.ID, m.Title,
			strings.ReplaceAll(strings.TrimSpace(m.Text), "\n", "\n    "))
		sent.Record(m, c.Now, forDay) // kept in the sandbox so dedup and the cap behave across --now runs
		return nil
	}
	switch m.Channel {
	case pulse.ChanInbox:
		data, err := pulse.InboxMarkdown(m, c.Today)
		if err != nil {
			return err
		}
		path := pulse.InboxFile(dir, m.ID)
		if err := c.MkdirAll(filepath.Dir(path)); err != nil {
			return err
		}
		if err := c.WriteFile(path, data); err != nil {
			return err
		}
	case pulse.ChanTelegram:
		if err := c.Notify(outbox.Notice, m.Title, m.Text); err != nil {
			return err
		}
	}
	sent.Record(m, c.Now, forDay)
	c.Log.Info("Pulse %s → %s: %s", m.Type, m.Channel, m.ID)
	return nil
}

// pulseSave writes a state file (YAML with a header line).
func pulseSave(c *job.Ctx, path, header string, v any) error {
	data, err := pulse.Encode(header, v)
	if err != nil {
		return err
	}
	return pulseWrite(c, path, data)
}

// pulseWrite writes Pulse's own files. A sandbox dry run (--rahi-path) still writes them,
// so baselines and rollups build up there; a dry run on the real vault only prints diffs.
func pulseWrite(c *job.Ctx, path string, data []byte) error {
	if c.DryRun && c.Sandbox {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return store.AtomicWrite(path, data)
	}
	if err := c.MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	return c.WriteFile(path, data)
}

// day8 is YYYYMMDD for ids.
func day8(d time.Time) string { return d.Format("20060102") }
