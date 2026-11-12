package pulse

import (
	"os"
	"path/filepath"
	"time"
)

// Dirs are the folders Pulse works with (kronos.yml paths). The *Old ones are the RahiOS
// folders, read only as a fallback so history from before Kshan still counts; "" = none.
type Dirs struct {
	Home     string // paths.pulse: goals.md, baseline.md, state/, weekly/, monthly/
	Daily    string // paths.daily_logs: YYYYMMDD.md (Kshan writes)
	DailyOld string // paths.daily_logs_old
	Inbox    string // paths.phone_inbox: messages Kronos writes (+ read/ moved by the phone)
	InboxOld string // paths.phone_inbox_old: read/ feedback only
}

// SandboxPaths point every Pulse folder into one test folder (daily/, inbox/, Pulse's own
// files at the top, no fallbacks), as kronos run --sandbox and the tests do.
func SandboxPaths(paths map[string]string, dir string) {
	paths["pulse"] = dir
	paths["daily_logs"] = filepath.Join(dir, "daily")
	paths["daily_logs_old"] = ""
	paths["phone_inbox"] = filepath.Join(dir, "inbox")
	paths["phone_inbox_old"] = ""
}

// Day is the log file for a date: the new folder, else the old one if only it has the day.
func (d Dirs) Day(t time.Time) string {
	p := DayFile(d.Daily, t)
	if d.DailyOld != "" {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			if old := DayFile(d.DailyOld, t); exists(old) {
				return old
			}
		}
	}
	return p
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
