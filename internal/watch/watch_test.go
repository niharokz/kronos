package watch

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.com/niharokz/kronos/internal/config"
	"gitlab.com/niharokz/kronos/internal/dates"
	"gitlab.com/niharokz/kronos/internal/job"
)

// testCtx: a job context with watch, rahi and outbox in a temp folder.
func testCtx(t *testing.T, options string, dry bool) (*job.Ctx, string, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.Parse([]byte(`paths:
  logs: `+dir+`/logs
  state: `+dir+`/state.json
  outbox: `+dir+`/outbox
  watch: `+dir+`/watch
  rahi: `+dir+`/rahi
jobs:
  watch_test: {schedule: '@manual', options: {`+options+`}}
`), "test.yml")
	if err != nil {
		t.Fatal(err)
	}
	j, _ := cfg.Job("watch_test")
	out := &bytes.Buffer{}
	c := job.NewCtx(context.Background(), cfg, j, dry, out)
	c.Now = time.Date(2026, 10, 26, 11, 15, 0, 0, time.Local)
	c.Today = dates.Today(c.Now)
	return c, dir, out
}

// fake is a monitor whose value the test sets.
type fake struct {
	Value int `yaml:"value"`
}

func fakeWatcher(v *int, err *error, warns *[]string) Watcher[fake] {
	return Watcher[fake]{
		Name: "test", Title: "Test", Notify: []string{Telegram, Rahi},
		Fetch: func(*job.Ctx, *Client) (fake, []string, error) { return fake{*v}, *warns, *err },
		Show:  func(f fake) string { return "value " + Commas(float64(f.Value)) },
		Diff: func(_ *job.Ctx, o, n fake) string {
			if o.Value == n.Value {
				return ""
			}
			return "value " + Commas(float64(o.Value)) + " → " + Commas(float64(n.Value))
		},
	}
}

func count(t *testing.T, pattern string) int {
	t.Helper()
	files, _ := filepath.Glob(pattern)
	return len(files)
}

// Baseline, then: same value → no write and no message; a change → stored + Telegram + Rahi inbox.
func TestRunStoresAndNotifiesOnlyOnChange(t *testing.T) {
	c, dir, out := testCtx(t, "", false)
	v, ferr, warns := 100, error(nil), []string(nil)
	w := fakeWatcher(&v, &ferr, &warns)
	state := filepath.Join(dir, "watch", "test.yml")

	if err := Run(c, w); err != nil {
		t.Fatal(err)
	}
	if count(t, dir+"/outbox/*.json") != 1 || count(t, dir+"/rahi/inbox/watch-test-start*.md") != 1 {
		t.Fatalf("baseline should announce once on both channels\n%s", out)
	}
	before, _ := os.Stat(state)

	time.Sleep(20 * time.Millisecond)
	if err := Run(c, w); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(state)
	if !after.ModTime().Equal(before.ModTime()) || count(t, dir+"/outbox/*.json") != 1 {
		t.Error("same value rewrote the file or sent a message")
	}

	v = 120
	c.Now = c.Now.Add(time.Hour)
	if err := Run(c, w); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(state)
	if !strings.Contains(string(raw), "value: 120") || count(t, dir+"/outbox/*.json") != 2 {
		t.Errorf("change not stored/sent:\n%s", raw)
	}
	inbox, _ := os.ReadFile(filepath.Join(dir, "rahi", "inbox", "watch-test-202610261215.md"))
	if !strings.HasPrefix(string(inbox), "---\nid: watch-test-202610261215\ntitle: Test\nfrom: Kronos\n") || !strings.Contains(string(inbox), "value 100 → 120") {
		t.Errorf("rahi inbox:\n%s", inbox)
	}
}

// A dry run prints the values and what it would do, and stores nothing.
func TestRunDryRunStoresNothing(t *testing.T) {
	c, dir, out := testCtx(t, "", true)
	v, ferr, warns := 7, error(nil), []string(nil)
	if err := Run(c, fakeWatcher(&v, &ferr, &warns)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "value 7") || !strings.Contains(out.String(), "first run") {
		t.Errorf("output:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "watch")); err == nil || count(t, dir+"/outbox/*") != 0 {
		t.Error("dry run wrote something")
	}
}

// options.notify picks the channels.
func TestRunNotifyOption(t *testing.T) {
	c, dir, _ := testCtx(t, "notify: [rahi]", false)
	v, ferr, warns := 1, error(nil), []string(nil)
	_ = Run(c, fakeWatcher(&v, &ferr, &warns))
	if count(t, dir+"/outbox/*.json") != 0 || count(t, dir+"/rahi/inbox/*.md") != 1 {
		t.Error("notify: [rahi] should skip Telegram")
	}
}

// Failures warn; the fail_after-th in a row fails the job once; success resets.
func TestRunFailures(t *testing.T) {
	c, dir, out := testCtx(t, "fail_after: 2", false)
	v, ferr, warns := 1, errors.New("HTTP 503"), []string(nil)
	w := fakeWatcher(&v, &ferr, &warns)
	if err := Run(c, w); err != nil {
		t.Fatalf("first failure should only warn: %v", err)
	}
	if err := Run(c, w); err == nil || !strings.Contains(err.Error(), "2 checks in a row failed") {
		t.Fatalf("second failure should alert: %v", err)
	}
	if err := Run(c, w); err != nil {
		t.Fatalf("third failure should not alert again: %v", err)
	}
	ferr = nil
	_ = Run(c, w)
	raw, _ := os.ReadFile(failuresFile(filepath.Join(dir, "watch")))
	if strings.Contains(string(raw), "test") || !strings.Contains(out.String(), "Working again after 3") {
		t.Errorf("not reset: %s\n%s", raw, out)
	}
}

// A partial fetch never counts as a change unless the monitor merges it (Keep).
func TestRunPartialFetch(t *testing.T) {
	c, dir, _ := testCtx(t, "", false)
	v, ferr, warns := 1, error(nil), []string(nil)
	w := fakeWatcher(&v, &ferr, &warns)
	_ = Run(c, w)
	v, warns = 2, []string{"page 2 failed"}
	_ = Run(c, w)
	raw, _ := os.ReadFile(filepath.Join(dir, "watch", "test.yml"))
	if !strings.Contains(string(raw), "value: 1") || count(t, dir+"/outbox/*.json") != 1 {
		t.Errorf("partial fetch was stored:\n%s", raw)
	}
}

func TestHelpers(t *testing.T) {
	if s := Text(`<p>₹&nbsp;13,479 <b>per</b><script>x()</script> gram</p>`); s != "₹ 13,479 per gram" {
		t.Errorf("Text %q", s)
	}
	if v, ok := Number("₹1,47,153.50"); !ok || v != 147153.5 {
		t.Errorf("Number %v", v)
	}
	if Commas(13479) != "13,479" || Commas(1234567.5) != "1,234,567.50" {
		t.Error("Commas")
	}
	if Attr(`class="a b" href='/x/y'`, "href") != "/x/y" {
		t.Error("Attr")
	}
	if s := clip("aaaa\nbbbb\ncccc", 11); s != "aaaa\nbbbb\n…" {
		t.Errorf("clip %q", s)
	}
}
