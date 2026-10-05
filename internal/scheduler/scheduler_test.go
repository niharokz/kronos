package scheduler

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.com/niharokz/kronos/internal/config"
	"gitlab.com/niharokz/kronos/internal/job"
	"gitlab.com/niharokz/kronos/internal/state"
)

func init() {
	job.Register("test_ok", func(c *job.Ctx) error { c.Log.OK("fine"); return nil })
	job.Register("test_fail", func(c *job.Ctx) error { return errors.New("something broke") })
	job.Register("test_panic", func(c *job.Ctx) error { panic("boom") })
	job.Register("test_slow", func(c *job.Ctx) error { time.Sleep(3 * time.Second); return nil })
}

func testConfig(t *testing.T, jobs string) (*config.Config, string) {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.Parse([]byte("paths:\n  logs: "+dir+"/logs\n  state: "+dir+"/state.json\n  outbox: "+dir+"/outbox\n  events: "+dir+"/event.md\njobs:\n"+jobs), dir+"/kronos.yml")
	if err != nil {
		t.Fatal(err)
	}
	return cfg, dir
}

func outboxMessages(t *testing.T, dir string) []map[string]any {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "outbox", "*.json"))
	var msgs []map[string]any
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		var m map[string]any
		json.Unmarshal(raw, &m)
		msgs = append(msgs, m)
	}
	return msgs
}

func TestExecuteOutcomes(t *testing.T) {
	cfg, _ := testConfig(t, "  test_slow: {schedule: '@manual', timeout: 1}\n")
	var out bytes.Buffer
	if err := Execute(cfg, config.Job{Name: "test_ok"}, false, &out); err != nil {
		t.Errorf("ok job: %v", err)
	}
	if err := Execute(cfg, config.Job{Name: "test_fail"}, false, &out); err == nil || err.Error() != "something broke" {
		t.Errorf("fail job: %v", err)
	}
	if err := Execute(cfg, config.Job{Name: "test_panic"}, false, &out); err == nil || !strings.Contains(err.Error(), "crashed: boom") {
		t.Errorf("panic job: %v", err)
	}
	slow, _ := cfg.Job("test_slow")
	start := time.Now()
	if err := Execute(cfg, slow, false, &out); err == nil || !strings.Contains(err.Error(), "Timed out after 1s") {
		t.Errorf("slow job: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("timeout did not return promptly")
	}
	if err := Execute(cfg, config.Job{Name: "nope"}, false, &out); err == nil {
		t.Error("unregistered job should fail")
	}
}

func TestCommandJob(t *testing.T) {
	cfg, dir := testConfig(t, `  script_ok:
    schedule: '@manual'
    command: ["sh", "-c", "echo $KRONOS_JOB $KRONOS_DRY_RUN > $KRONOS_LOGS.out"]
  script_fail:
    schedule: '@manual'
    command: ["sh", "-c", "echo line1 >&2; echo disk full >&2; exit 3"]
  script_slow:
    schedule: '@manual'
    timeout: 1
    command: ["sleep", "5"]
`)
	var out bytes.Buffer
	ok, _ := cfg.Job("script_ok")
	if err := Execute(cfg, ok, false, &out); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(dir + "/logs.out"); strings.TrimSpace(string(raw)) != "script_ok 0" {
		t.Errorf("env not passed: %q", raw)
	}
	bad, _ := cfg.Job("script_fail")
	if err := Execute(cfg, bad, false, &out); err == nil || err.Error() != "Exited with code 3\nline1\ndisk full" {
		t.Errorf("failing script: %v", err)
	}
	slow, _ := cfg.Job("script_slow")
	if err := Execute(cfg, slow, false, &out); err == nil || !strings.Contains(err.Error(), "Timed out after 1s – killed") {
		t.Errorf("slow script: %v", err)
	}
}

func TestBadJobsAreSkippedAndAlerted(t *testing.T) {
	cfg, dir := testConfig(t, `  good: {schedule: '@manual', command: [true]}
  badcron: {schedule: '61 * * * *', command: [true]}
  ghost: {schedule: '0 2 * * *'}
  off: {schedule: '0 2 * * *', enabled: false}
`)
	s := New(cfg, &bytes.Buffer{})
	if len(s.tasks) != 1 || s.tasks[0].job.Name != "good" {
		t.Fatalf("tasks = %d", len(s.tasks))
	}
	titles := map[string]bool{}
	for _, m := range outboxMessages(t, dir) {
		if m["kind"] == "alert" {
			titles[m["title"].(string)] = true
		}
	}
	if !titles["badcron"] || !titles["ghost"] || titles["off"] {
		t.Errorf("alerts = %v", titles)
	}
}

func TestCatchUpRunsMissedSlotsInOrder(t *testing.T) {
	cfg, dir := testConfig(t, `  first: {schedule: '0 2 * * *', command: [true]}
  second: {schedule: '30 2 * * *', command: [true]}
  never_ran: {schedule: '0 3 * * *', command: [true]}
  opted_out: {schedule: '0 2 * * *', catch_up: false, command: [true]}
  fresh: {schedule: '0 2 * * *', command: [true]}
`)
	now := time.Date(2026, 10, 5, 9, 0, 0, 0, time.Local)
	lastNight := time.Date(2026, 10, 4, 2, 0, 0, 0, time.Local)
	for _, name := range []string{"second", "first", "opted_out"} {
		state.Record(dir+"/state.json", name, lastNight, time.Second, nil)
	}
	state.Record(dir+"/state.json", "fresh", now.Add(-time.Hour), time.Second, nil) // already ran today

	s := New(cfg, &bytes.Buffer{})
	s.catchUp(now)
	var queued []string
	for len(s.queue) > 0 {
		r := <-s.queue
		queued = append(queued, r.t.job.Name+" ("+r.reason+")")
	}
	want := []string{"first (catch-up for 2026-10-05 02:00)", "second (catch-up for 2026-10-05 02:30)"}
	if strings.Join(queued, "|") != strings.Join(want, "|") {
		t.Errorf("queued = %v\nwant     %v", queued, want)
	}
}

func TestPrev(t *testing.T) {
	_, sched, _ := ParseSchedule("40 5 * * *")
	now := time.Date(2026, 10, 5, 9, 0, 0, 0, time.Local)
	if got := Prev(sched, now, 48*time.Hour); !got.Equal(time.Date(2026, 10, 5, 5, 40, 0, 0, time.Local)) {
		t.Errorf("Prev = %v", got)
	}
	_, yearly, _ := ParseSchedule("30 3 1 1 *")
	if got := Prev(yearly, now, 48*time.Hour); !got.IsZero() {
		t.Errorf("yearly outside window = %v", got)
	}
}

func TestWatchTriggersAfterQuietPeriod(t *testing.T) {
	watched := filepath.Join(t.TempDir(), "watched.md")
	cfg, _ := testConfig(t, "  w: {schedule: '@manual', command: [true], watch: ["+watched+"]}\n")
	s := New(cfg, &bytes.Buffer{})
	w := s.tasks[0].watches[0]
	os.WriteFile(w.path, []byte("v1"), 0o644)
	t0 := time.Now()
	s.checkWatches(t0) // change seen
	if len(s.queue) != 0 {
		t.Fatal("fired before the quiet period")
	}
	s.checkWatches(t0.Add(5 * time.Second)) // still settling (tick is 10s)
	if len(s.queue) != 0 {
		t.Fatal("fired too early")
	}
	s.checkWatches(t0.Add(11 * time.Second))
	if len(s.queue) != 1 {
		t.Fatal("did not fire after the quiet period")
	}
	if r := <-s.queue; r.reason != "changed: watched.md" {
		t.Errorf("reason = %q", r.reason)
	}
}
