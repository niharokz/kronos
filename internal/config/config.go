// Package config loads kronos.yml: paths, global settings, and the job list.
//
// Jobs keep the order they have in the file. That order decides which job
// runs first when two are due in the same minute, and the order of
// @startup jobs — the same as Taskmaster's schedule.yml.
package config

import (
	"fmt"
	"os"
	"regexp"
	"strconv"

	"gopkg.in/yaml.v3"
)

// DefaultPath is where the container finds its config (override: KRONOS_CONFIG).
const DefaultPath = "/app/kronos.yml"

// Config is the parsed kronos.yml.
type Config struct {
	File     string
	Paths    map[string]string
	Settings Settings
	Jobs     []Job
}

// Settings are global knobs (settings: in kronos.yml).
type Settings struct {
	TickSeconds    int  `yaml:"tick_seconds"`    // how often the scheduler wakes up
	TimeoutSeconds int  `yaml:"timeout_seconds"` // default per-job timeout
	CatchUpHours   int  `yaml:"catch_up_hours"`  // how far back a missed run is still caught up on boot
	Alerts         bool `yaml:"alerts"`          // failures → Telegram via Omnimo
}

// Job is one entry under jobs:.
type Job struct {
	Name        string         `yaml:"-"`
	Schedule    string         `yaml:"schedule"`     // cron (5 fields) | @startup | @daemon | @manual
	Description string         `yaml:"description"`  //
	Enabled     *bool          `yaml:"enabled"`      // default true
	Timeout     int            `yaml:"timeout"`      // seconds; 0 = settings.timeout_seconds
	CatchUp     *bool          `yaml:"catch_up"`     // default true (cron jobs only)
	SkipIfBusy  bool           `yaml:"skip_if_busy"` // cron: skip a slot while another job runs or waits
	RunAtStart  bool           `yaml:"run_at_start"` // cron: also run once when Kronos starts
	Watch       []string       `yaml:"watch"`        // also run when any of these files change
	Command     []string       `yaml:"command"`      // run a program instead of a built-in Go job
	Options     map[string]any `yaml:"options"`      // job-specific settings, read with Ctx.Int/String/...
}

// IsEnabled reports whether the job should be scheduled at all.
func (j Job) IsEnabled() bool { return j.Enabled == nil || *j.Enabled }

// CatchesUp reports whether a missed cron run is caught up on boot.
func (j Job) CatchesUp() bool { return j.CatchUp == nil || *j.CatchUp }

// defaultPaths are used for any key missing from paths: in kronos.yml (container
// paths; the host folders behind them come from .env via docker-compose.yml).
var defaultPaths = map[string]string{
	"logs":             "/data/notes/logs/kronos",
	"state":            "/data/notes/logs/kronos/state.json",
	"outbox":           "/data/notes/data/outbox",
	"events":           "/data/notes/data/event.md",
	"event_backup":     "/data/notes/data/event.md.bck",
	"subscriptions":    "/data/notes/data/subscription.md",
	"calendar":         "/data/notes/data/calendar.md",
	"health":           "/data/notes/data/health.md",
	"inbox":            "/data/notes/inbox",
	"archive":          "/data/notes/archive",
	"daily":            "/data/notes/inbox/daily.md",
	"finance_inbox":    "/data/notes/data/finance/inbox",
	"vault":            "/data/notes",                      // notes vault root (relative job options resolve here)
	"pulse":            "/data/notes/logs/pulse",           // Pulse's own files: goals, baseline, state, rollups
	"daily_logs":       "/data/notes/logs/kshan/daily",     // phone daily logs (Kshan writes)
	"daily_logs_old":   "/data/notes/logs/rahi/daily",      // read-only fallback (RahiOS); "" = none
	"phone_inbox":      "/data/notes/logs/kshan/inbox",     // messages for the phone (Kshan Inbox widget)
	"phone_inbox_old":  "/data/notes/logs/rahi/inbox",      // read-only: read/ feedback of older messages; "" = none
	"kshan_events":     "/data/notes/logs/kshan/events.md", // event drafts from the phone
	"kshan_events_old": "/data/notes/logs/rahi/events.md",  // read-only fallback (RahiOS); "" = none
	"watch":            "/data/notes/logs/watch",
	"health_site":      "/data/health-site",
	"backups":          "/data/backups", // .env KRONOS_BACKUP is mounted here (notes_backup)
	"google_token":     "/app/config/token.json",
}

// Load reads and validates a kronos.yml.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return Parse(raw, path)
}

// Path returns the config file to use: $KRONOS_CONFIG or the default.
func Path() string {
	if p := os.Getenv("KRONOS_CONFIG"); p != "" { // env: KRONOS_CONFIG overrides /app/kronos.yml
		return p
	}
	return DefaultPath
}

// Parse parses kronos.yml contents (file is used in error messages).
//
// Values may use ${KRONOS_NAME} or ${KRONOS_NAME:-default}: they are replaced by that
// container environment variable (docker-compose.yml passes it from .env), so personal
// values such as a home location stay out of kronos.yml. Only names starting with
// KRONOS_ are expanded, and only in values (never in keys).
func Parse(raw []byte, file string) (*Config, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("config %s: %v", file, err)
	}
	cfg := &Config{
		File:  file,
		Paths: map[string]string{},
		Settings: Settings{
			TickSeconds:    10,
			TimeoutSeconds: 900,
			CatchUpHours:   48,
			Alerts:         true,
		},
	}
	for k, v := range defaultPaths {
		cfg.Paths[k] = v
	}
	if len(doc.Content) == 0 { // empty file: defaults, no jobs
		return cfg, nil
	}
	if err := checkKeys(&doc, file); err != nil { // catch typos like "schedul:" instead of silently ignoring them
		return nil, err
	}
	expandEnv(doc.Content[0])

	var top struct {
		Paths    map[string]string `yaml:"paths"`
		Settings *Settings         `yaml:"settings"` // decoded over the defaults: unset keys keep them
		Jobs     yaml.Node         `yaml:"jobs"`
	}
	top.Settings = &cfg.Settings
	if err := doc.Decode(&top); err != nil {
		return nil, fmt.Errorf("config %s: %v", file, err)
	}
	for k, v := range top.Paths {
		cfg.Paths[k] = v
	}
	if cfg.Settings.TickSeconds < 1 {
		cfg.Settings.TickSeconds = 10
	}

	if top.Jobs.Kind != 0 && top.Jobs.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config %s: jobs must be a mapping of job name → settings", file)
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(top.Jobs.Content); i += 2 {
		name := top.Jobs.Content[i].Value
		var j Job
		if err := top.Jobs.Content[i+1].Decode(&j); err != nil {
			return nil, fmt.Errorf("config %s: job %q: %v", file, name, err)
		}
		if seen[name] {
			return nil, fmt.Errorf("config %s: job %q is listed twice", file, name)
		}
		seen[name] = true
		j.Name = name
		if j.Schedule == "" {
			return nil, fmt.Errorf("config %s: job %q has no schedule", file, name)
		}
		cfg.Jobs = append(cfg.Jobs, j)
	}
	return cfg, nil
}

// envRef matches ${KRONOS_NAME} and ${KRONOS_NAME:-default}.
var envRef = regexp.MustCompile(`\$\{(KRONOS_[A-Z0-9_]+)(?::-([^}]*))?\}`)

// expandEnv replaces ${KRONOS_…} in every scalar value below n (mapping keys are left alone).
// A plain (unquoted) scalar is re-typed after expansion, so ${KRONOS_LAT} → 12.5 is a number
// and an unset variable without a default is null (the job then uses its own default).
func expandEnv(n *yaml.Node) {
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			expandEnv(c)
		}
	case yaml.MappingNode:
		for i := 1; i < len(n.Content); i += 2 {
			expandEnv(n.Content[i])
		}
	case yaml.ScalarNode:
		if !envRef.MatchString(n.Value) {
			return
		}
		n.Value = envRef.ReplaceAllStringFunc(n.Value, func(ref string) string {
			m := envRef.FindStringSubmatch(ref)
			if v := os.Getenv(m[1]); v != "" { // env: KRONOS_* from docker-compose.yml environment:
				return v
			}
			return m[2]
		})
		if n.Style == 0 {
			n.Tag = "" // resolve again: number, bool, or null when empty
		}
	}
}

// Allowed keys at each level of kronos.yml. paths: and options: are free-form
// (extra paths are passed to script jobs as KRONOS_<NAME>).
var (
	topKeys      = keySet("paths", "settings", "jobs")
	settingsKeys = keySet("tick_seconds", "timeout_seconds", "catch_up_hours", "alerts")
	jobKeys      = keySet("schedule", "description", "enabled", "timeout", "catch_up", "skip_if_busy", "run_at_start", "watch", "command", "options")
)

func keySet(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// checkKeys rejects unknown keys at the top level, under settings: and in
// each job, so a misspelt key fails loudly instead of being ignored.
func checkKeys(doc *yaml.Node, file string) error {
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("config %s: top level must be a mapping (paths, settings, jobs)", file)
	}
	if err := unknownKey(root, topKeys, file, "top level"); err != nil {
		return err
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, val := root.Content[i].Value, root.Content[i+1]
		switch {
		case key == "settings" && val.Kind == yaml.MappingNode:
			if err := unknownKey(val, settingsKeys, file, "settings"); err != nil {
				return err
			}
		case key == "jobs" && val.Kind == yaml.MappingNode:
			for j := 0; j+1 < len(val.Content); j += 2 {
				if body := val.Content[j+1]; body.Kind == yaml.MappingNode {
					if err := unknownKey(body, jobKeys, file, fmt.Sprintf("job %q", val.Content[j].Value)); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func unknownKey(m *yaml.Node, allowed map[string]bool, file, where string) error {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if k := m.Content[i]; !allowed[k.Value] {
			return fmt.Errorf("config %s: line %d: unknown key %q in %s", file, k.Line, k.Value, where)
		}
	}
	return nil
}

// Job returns the named job's config.
func (c *Config) Job(name string) (Job, bool) {
	for _, j := range c.Jobs {
		if j.Name == name {
			return j, true
		}
	}
	return Job{}, false
}

// ── Job options (options: under a job) ─────────────────────────────────

// Int returns options[key] as an int, or def.
func (j Job) Int(key string, def int) int {
	switch v := j.Options[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	case string:
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

// String returns options[key] as a string, or def.
func (j Job) String(key, def string) string {
	if v, ok := j.Options[key]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return def
}

// Bool returns options[key] as a bool, or def.
func (j Job) Bool(key string, def bool) bool {
	if v, ok := j.Options[key].(bool); ok {
		return v
	}
	return def
}

// Strings returns options[key] as a list of strings, or def.
func (j Job) Strings(key string, def []string) []string {
	list, ok := j.Options[key].([]any)
	if !ok {
		return def
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		out = append(out, fmt.Sprint(v))
	}
	return out
}
