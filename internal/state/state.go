// Package state remembers each job's last run in a small JSON file
// (paths.state). The scheduler uses it to catch up runs missed while the
// host was off; `kronos health` and `kronos list` report from it.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gitlab.com/niharokz/kronos/internal/store"
)

// Status values.
const (
	OK   = "ok"
	Fail = "fail"
)

// Job is one job's last-run record.
type Job struct {
	LastRun    time.Time `json:"last_run"`
	LastStatus string    `json:"last_status"`
	LastOK     time.Time `json:"last_ok,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
	DurationMs int64     `json:"duration_ms"`
}

// File is the whole state file.
type File struct {
	Jobs map[string]Job `json:"jobs"`
}

// Load reads the state file; a missing file is an empty state.
func Load(path string) (File, error) {
	f := File{Jobs: map[string]Job{}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, fmt.Errorf("state: %w", err)
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return File{Jobs: map[string]Job{}}, fmt.Errorf("state: %s is corrupt (%v) — starting fresh", path, err)
	}
	if f.Jobs == nil {
		f.Jobs = map[string]Job{}
	}
	return f, nil
}

// Record stores one run's outcome (atomic write).
func Record(path, name string, started time.Time, duration time.Duration, runErr error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("state: %w", err)
	}
	f, _ := Load(path) // a corrupt file is replaced
	j := f.Jobs[name]
	j.LastRun = started
	j.DurationMs = duration.Milliseconds()
	if runErr == nil {
		j.LastStatus = OK
		j.LastOK = started
		j.LastError = ""
	} else {
		j.LastStatus = Fail
		j.LastError = runErr.Error()
	}
	f.Jobs[name] = j

	body, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}
	return store.AtomicWrite(path, append(body, '\n'))
}
