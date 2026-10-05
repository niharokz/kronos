package jobs

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gitlab.com/niharokz/kronos/internal/job"
	"gitlab.com/niharokz/kronos/internal/outbox"
)

// restarted sends a Telegram notice when Kronos starts, with the time, host
// uptime (tells a host reboot apart from a container restart) and RAM.
//
// Options: label (default "Nimory") — the host name shown in the message.

func init() { job.Register("restarted", restarted) }

func restarted(c *job.Ctx) error {
	used, total, percent, err := ramUsage()
	if err != nil {
		return err
	}
	uptime, err := hostUptime()
	if err != nil {
		return err
	}
	// kronos.yml: jobs.restarted.options.label (host name in the message)
	msg := fmt.Sprintf("🏠 %s: Kronos started\n\nTime: %s\nHost up: %s\nRAM: %.1f GB / %.1f GB (%.0f%%)",
		c.String("label", "Nimory"), c.Now.Format("2006-01-02 15:04:05"), uptime, used, total, percent)
	if err := c.Notify(outbox.Notice, "", msg); err != nil {
		return err
	}
	c.Log.OK("Startup notice sent")
	return nil
}

// ramUsage reads /proc/meminfo: used and total GB and percent used.
func ramUsage() (used, total, percent float64, err error) {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, 0, err
	}
	mem := map[string]float64{}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if fields := strings.Fields(value); len(fields) > 0 {
			kb, _ := strconv.ParseFloat(fields[0], 64)
			mem[key] = kb
		}
	}
	if mem["MemTotal"] == 0 {
		return 0, 0, 0, fmt.Errorf("cannot read MemTotal from /proc/meminfo")
	}
	total = mem["MemTotal"] / 1024 / 1024
	used = total - mem["MemAvailable"]/1024/1024
	return used, total, used / total * 100, nil
}

// hostUptime is "Xh Ym" from /proc/uptime (not namespaced: the host's uptime).
func hostUptime() (string, error) {
	raw, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return "", err
	}
	secs, err := strconv.ParseFloat(strings.Fields(string(raw))[0], 64)
	if err != nil {
		return "", err
	}
	s := int(secs)
	return fmt.Sprintf("%dh %dm", s/3600, s%3600/60), nil
}
