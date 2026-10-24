package jobs

import (
	"fmt"
	"os"
	"strings"

	"gitlab.com/niharokz/kronos/internal/job"
	"gitlab.com/niharokz/kronos/internal/outbox"
	"gitlab.com/niharokz/kronos/internal/pulse"
)

// nimo_briefing queues the day's agenda (paths.daily) as an Omnimo
// briefing. Omnimo sends the agenda, then adds Nimo's commentary with its
// own persona and AI — Kronos has no AI code. When Pulse is set up, its brief
// (yesterday's verdict, today's targets, deferred warnings) is appended.

func init() { job.Register("nimo_briefing", nimoBriefing) }

func nimoBriefing(c *job.Ctx) error {
	path := c.Path("daily") // kronos.yml: paths.daily
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("Missing file: %s", path)
	}
	text, err := readText(path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		c.Log.Skip("daily.md is empty")
		return nil
	}
	if b, ok := pulse.LoadBrief(pulse.BriefFile(c.Path("rahi")), c.Today); ok { // kronos.yml: paths.rahi (Pulse, if used)
		text = strings.TrimRight(text, "\n") + "\n\n" + b.Text()
	}
	if err := c.Notify(outbox.Briefing, "daily", text); err != nil {
		return err
	}
	c.Log.OK("Briefing queued for Omnimo")
	return nil
}
