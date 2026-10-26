// Package watch holds Kronos's monitors: jobs named watch_<name> that check something
// outside the homelab (a web page, a price) and tell you only when it changes.
//
// Each monitor is one file here (<name>.go + <name>_test.go) plus one jobs: entry in
// kronos.yml. A monitor supplies Fetch (current values), Show (them as text) and Diff
// (what changed worth telling); Run does the rest:
//
//   - the latest values are kept in <paths.watch>/<name>.yml, rewritten only when they change;
//   - a change worth telling goes to Telegram (via Omnimo) and/or the Rahi inbox (options.notify);
//   - the first run only records a baseline and says what it is now watching;
//   - a failed fetch is a warning; options.fail_after failures in a row (default 3) alert once;
//   - a dry run (kronos run watch_<name> --dry-run) fetches and prints, and stores nothing.
//
// External calls stay in this package; the rest of Kronos never imports it.
package watch

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"gitlab.com/niharokz/kronos/internal/dates"
	"gitlab.com/niharokz/kronos/internal/job"
	"gitlab.com/niharokz/kronos/internal/outbox"
	"gitlab.com/niharokz/kronos/internal/store"
)

// Notification channels (options.notify).
const (
	Telegram = "telegram" // Omnimo notice
	Rahi     = "rahi"     // <paths.rahi>/inbox/<id>.md
)

// Watcher is one monitor's logic; T is what it stores (YAML-marshalled).
type Watcher[T any] struct {
	Name   string                                           // job is watch_<Name>; state is <Name>.yml
	Title  string                                           // first line of every message
	Notify []string                                         // default channels when options.notify is unset
	Fetch  func(c *job.Ctx, h *Client) (T, []string, error) // current values + warnings (a source failed)
	Show   func(cur T) string                               // current values as text
	Diff   func(c *job.Ctx, old, cur T) string              // change worth telling; "" = none
	Keep   func(old, cur T) T                               // optional: merge a partial fetch with the old values
}

// Register adds the monitor as job watch_<Name>.
func Register[T any](w Watcher[T]) {
	job.Register("watch_"+w.Name, func(c *job.Ctx) error { return Run(c, w) })
}

// stateFile is what is stored on disk: the values plus when they last changed.
type stateFile[T any] struct {
	Updated string `yaml:"updated"`
	Data    T      `yaml:"data"`
}

// Run fetches, compares with the stored values, stores and notifies on change.
func Run[T any](c *job.Ctx, w Watcher[T]) error {
	dir := c.Path("watch") // kronos.yml: paths.watch
	path := filepath.Join(dir, w.Name+".yml")
	h := NewClient(c)

	cur, warns, err := w.Fetch(c, h)
	if err != nil {
		return failed(c, dir, w.Name, err)
	}
	for _, m := range warns {
		c.Log.Warn("%s", m)
	}
	old, had, err := load[T](path)
	if err != nil { // a broken state file is never overwritten blindly
		return fmt.Errorf("%s: %v – fix or delete it", path, err)
	}

	if c.DryRun { // values only: nothing stored, nothing sent
		fmt.Fprintln(c.Out, w.Show(cur))
		switch {
		case !had:
			fmt.Fprintln(c.Out, "~ first run: would store this as the baseline")
		case same(old.Data, cur):
			fmt.Fprintln(c.Out, "~ same as stored: nothing to send")
		default:
			if msg := w.Diff(c, old.Data, cur); msg != "" {
				fmt.Fprintf(c.Out, "~ would notify %s:\n%s\n", strings.Join(channels(c, w), ", "), msg)
			} else {
				fmt.Fprintln(c.Out, "~ changed, but nothing worth telling: would store silently")
			}
		}
		return nil
	}
	recovered(c, dir, w.Name)

	if len(warns) > 0 && had {
		if w.Keep == nil { // a partial fetch must not look like a change
			c.Log.Skip("Incomplete fetch – kept the stored values")
			return nil
		}
		cur = w.Keep(old.Data, cur)
	}
	if !had {
		if err := save(path, cur, c.Now); err != nil {
			return err
		}
		c.Log.OK("Baseline stored")
		return send(c, w, "start", "now watching", w.Show(cur))
	}
	if same(old.Data, cur) {
		c.Log.Skip("No change")
		return nil
	}
	msg := w.Diff(c, old.Data, cur)
	if err := save(path, cur, c.Now); err != nil {
		return err
	}
	if msg == "" {
		c.Log.OK("Stored (nothing worth telling)")
		return nil
	}
	c.Log.OK("Changed – notified")
	return send(c, w, c.Now.Format("200601021504"), "", msg)
}

// channels is options.notify, else the monitor's default.
func channels[T any](c *job.Ctx, w Watcher[T]) []string {
	return c.Strings("notify", w.Notify) // kronos.yml: jobs.watch_<name>.options.notify
}

// send delivers a message on every configured channel.
func send[T any](c *job.Ctx, w Watcher[T], idSuffix, note, text string) error {
	title := w.Title
	if note != "" {
		title += " — " + note
	}
	for _, ch := range channels(c, w) {
		switch ch {
		case Telegram:
			if err := c.Notify(outbox.Notice, title, clip(title+"\n\n"+text, 3800)); err != nil {
				return err
			}
		case Rahi:
			if err := rahiInbox(c, "watch-"+w.Name+"-"+idSuffix, title, text); err != nil {
				return err
			}
		default:
			c.Log.Warn("Unknown notify channel %q (use telegram, rahi)", ch)
		}
	}
	return nil
}

// rahiInbox drops one message into Rahi's inbox (frontmatter title/from/date, like Pulse).
func rahiInbox(c *job.Ctx, id, title, text string) error {
	dir := filepath.Join(c.Path("rahi"), "inbox") // kronos.yml: paths.rahi
	head, err := yaml.Marshal(struct {
		ID    string `yaml:"id"`
		Title string `yaml:"title"`
		From  string `yaml:"from"`
		Date  string `yaml:"date"`
		Type  string `yaml:"type"`
	}{id, title, "Kronos", dates.Format(c.Today), "watch"})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	body := "---\n" + string(head) + "---\n" + strings.TrimSpace(text) + "\n"
	return store.AtomicWrite(store.UniquePath(dir, id+".md"), []byte(body))
}

// ── State ───────────────────────────────────────────────────────────────

func load[T any](path string) (stateFile[T], bool, error) {
	var st stateFile[T]
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, false, nil
	}
	if err != nil {
		return st, false, err
	}
	return st, true, yaml.Unmarshal(raw, &st)
}

func save[T any](path string, v T, now time.Time) error {
	var b bytes.Buffer
	b.WriteString("# Kronos monitor state — rewritten only when the values change\n")
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(stateFile[T]{Updated: now.Format("2006-01-02 15:04:05"), Data: v}); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return store.AtomicWrite(path, b.Bytes())
}

// same compares two values by their YAML form (what is stored).
func same[T any](a, b T) bool {
	x, err1 := yaml.Marshal(a)
	y, err2 := yaml.Marshal(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}

// ── Failures: warn, alert once after fail_after in a row ────────────────

func failuresFile(dir string) string { return filepath.Join(dir, "failures.yml") }

func failed(c *job.Ctx, dir, name string, err error) error {
	if c.DryRun {
		return err
	}
	counts := loadCounts(dir)
	counts[name]++
	saveCounts(c, dir, counts)
	limit := c.Int("fail_after", 3) // kronos.yml: jobs.watch_<name>.options.fail_after
	if counts[name] == limit {
		return fmt.Errorf("%d checks in a row failed: %v", limit, err) // FAIL + one alert
	}
	c.Log.Warn("Check failed (%d in a row): %v", counts[name], err)
	return nil
}

func recovered(c *job.Ctx, dir, name string) {
	counts := loadCounts(dir)
	n := counts[name]
	if n == 0 {
		return
	}
	delete(counts, name)
	saveCounts(c, dir, counts)
	c.Log.Info("Working again after %d failed check(s)", n)
}

func loadCounts(dir string) map[string]int {
	m := map[string]int{}
	if raw, err := os.ReadFile(failuresFile(dir)); err == nil {
		_ = yaml.Unmarshal(raw, &m)
	}
	return m
}

func saveCounts(c *job.Ctx, dir string, m map[string]int) {
	data, err := yaml.Marshal(m)
	if err == nil {
		err = os.MkdirAll(dir, 0o755)
	}
	if err == nil {
		err = store.AtomicWrite(failuresFile(dir), data)
	}
	if err != nil {
		c.Log.Warn("Failure count not saved: %v", err)
	}
}

// clip shortens text to n bytes on a line break (Telegram's limit is 4096).
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := strings.LastIndex(s[:n], "\n")
	if cut < n/2 {
		cut = n
	}
	return s[:cut] + "\n…"
}
