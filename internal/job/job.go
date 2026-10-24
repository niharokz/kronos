// Package job is the plug-in point for Kronos jobs.
//
// Adding a job takes one Go file plus one entry in kronos.yml:
//
//	// internal/jobs/my_job.go
//	package jobs
//
//	func init() { job.Register("my_job", myJob) }
//
//	func myJob(c *job.Ctx) error {
//	    // ... do the work with c.Path, c.WriteFile, c.Notify ...
//	    c.Log.OK("Done")
//	    return nil // an error → FAIL log + Telegram alert
//	}
//
//	# kronos.yml
//	jobs:
//	  my_job:
//	    schedule: "0 4 * * *"
//	    description: "What it does."
//
// Every job receives a *Ctx with its logger, configured paths and options,
// dry-run-aware file helpers, and Notify for
// Telegram (through Omnimo's outbox).
package job

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"gitlab.com/niharokz/kronos/internal/config"
	"gitlab.com/niharokz/kronos/internal/dates"
	"gitlab.com/niharokz/kronos/internal/outbox"
	"gitlab.com/niharokz/kronos/internal/store"
)

// Func is a job. Return nil on success (including "nothing to do"); return
// an error to mark the run FAIL and send an alert.
type Func func(c *Ctx) error

var (
	regMu    sync.Mutex
	registry = map[string]Func{}
)

// Register makes a job available under name. Call it from an init() in
// the job's own file. Registering the same name twice is a programming
// error and panics at startup.
func Register(name string, fn Func) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := registry[name]; dup {
		panic("job: " + name + " registered twice")
	}
	registry[name] = fn
}

// Lookup returns the job registered under name.
func Lookup(name string) (Func, bool) {
	regMu.Lock()
	defer regMu.Unlock()
	fn, ok := registry[name]
	return fn, ok
}

// Names lists all registered jobs, sorted.
func Names() []string {
	regMu.Lock()
	defer regMu.Unlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Source is the outbox "source" for everything Kronos sends.
const Source = "kronos"

// Ctx is everything a job may use.
type Ctx struct {
	context.Context

	Name   string    // job name (its key in kronos.yml)
	Log    *Logger   // [time] [LEVEL] [name] message
	Now    time.Time // when this run started (local time)
	Today  time.Time // today's civil date (see package dates)
	DryRun bool      // when true, file changes are printed instead of made
	Cfg    *config.Config
	Job    config.Job // this job's entry in kronos.yml
	Out    io.Writer  // where dry-run diffs and notices are printed

	Sandbox bool // kronos run --rahi-path: test folder; jobs may keep test state even in a dry run
}

// NewCtx builds the context for one run.
func NewCtx(parent context.Context, cfg *config.Config, j config.Job, dryRun bool, out io.Writer) *Ctx {
	now := time.Now()
	if out == nil {
		out = os.Stdout
	}
	logDir := cfg.Paths["logs"] // kronos.yml: paths.logs
	if dryRun {
		logDir = "" // dry runs print only, never touch the log file
	}
	return &Ctx{
		Context: parent,
		Name:    j.Name,
		Log:     NewLogger(j.Name, logDir, out),
		Now:     now,
		Today:   dates.Today(now),
		DryRun:  dryRun,
		Cfg:     cfg,
		Job:     j,
		Out:     out,
	}
}

// Path returns a configured path (paths: in kronos.yml). An unknown key is a
// setup mistake, so the run fails with a clear message.
func (c *Ctx) Path(key string) string {
	p, ok := c.Cfg.Paths[key]
	if !ok || p == "" {
		panic(fmt.Sprintf("unknown path %q — add it under paths: in %s", key, c.Cfg.File))
	}
	return p
}

// Int, String, Bool and Strings read this job's options: block.
func (c *Ctx) Int(key string, def int) int               { return c.Job.Int(key, def) }
func (c *Ctx) String(key, def string) string             { return c.Job.String(key, def) }
func (c *Ctx) Bool(key string, def bool) bool            { return c.Job.Bool(key, def) }
func (c *Ctx) Strings(key string, def []string) []string { return c.Job.Strings(key, def) }

// ── Telegram ────────────────────────────────────────────────────────────

// Notify queues a Telegram message via Omnimo's outbox. kind is
// outbox.Notice, outbox.Alert or outbox.Briefing.
func (c *Ctx) Notify(kind, title, text string) error {
	if c.DryRun {
		fmt.Fprintf(c.Out, "~ would send %s%s via Omnimo:\n%s\n", kind, titleSuffix(title), indent(text))
		return nil
	}
	_, err := outbox.Write(c.Path("outbox"), Source, kind, title, text) // kronos.yml: paths.outbox
	return err
}

func titleSuffix(t string) string {
	if t == "" {
		return ""
	}
	return " (" + t + ")"
}

// ── Files (dry-run aware) ───────────────────────────────────────────────

// WriteFile atomically replaces path. In a dry run it prints a diff instead.
func (c *Ctx) WriteFile(path string, data []byte) error {
	if c.DryRun {
		old, _ := os.ReadFile(path)
		fmt.Fprint(c.Out, Diff(path, string(old), string(data)))
		return nil
	}
	return store.AtomicWrite(path, data)
}

// AppendFile appends text to path (created if missing).
func (c *Ctx) AppendFile(path, text string) error {
	if c.DryRun {
		fmt.Fprintf(c.Out, "~ would append to %s:\n%s\n", path, indent(text))
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Move renames src to dst, falling back to copy+delete across filesystems.
func (c *Ctx) Move(src, dst string) error {
	if c.DryRun {
		fmt.Fprintf(c.Out, "~ would move %s → %s\n", src, dst)
		return nil
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, info.Mode().Perm()); err != nil {
		return err
	}
	_ = os.Chtimes(dst, info.ModTime(), info.ModTime())
	return os.Remove(src)
}

// Remove deletes a file.
func (c *Ctx) Remove(path string) error {
	if c.DryRun {
		fmt.Fprintf(c.Out, "~ would delete %s\n", path)
		return nil
	}
	return os.Remove(path)
}

// MkdirAll creates a folder and its parents.
func (c *Ctx) MkdirAll(path string) error {
	if c.DryRun {
		if _, err := os.Stat(path); err != nil {
			fmt.Fprintf(c.Out, "~ would create folder %s\n", path)
		}
		return nil
	}
	return os.MkdirAll(path, 0o755)
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n    ")
}
