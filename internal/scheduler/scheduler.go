// Package scheduler decides when jobs run.
//
// Schedules (schedule: in kronos.yml):
//
//	"30 2 * * *"  cron, 5 fields, container local time
//	@startup      once each time Kronos starts
//	@daemon       kept running; restarted on exit (30 s backoff, gives up after 5)
//	@manual       never on its own — only `kronos run <job>` or a watch
//
// Any job can also have watch: [files] — it then also runs ~10–20 s after
// one of those files changes (debounced, so a burst of saves runs it once).
//
// Jobs run one at a time, in the order they become due, exactly like
// Taskmaster: a job's next run is computed from when it finished, so an
// overrun skips a slot instead of piling up. On start, a cron job whose last
// scheduled slot (within settings.catch_up_hours) was missed — the host was
// off — runs once to catch up, in slot order.
package scheduler

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"gitlab.com/niharokz/kronos/internal/config"
	"gitlab.com/niharokz/kronos/internal/job"
	"gitlab.com/niharokz/kronos/internal/state"
)

// Kind is how a job is triggered.
type Kind int

const (
	Cron Kind = iota
	Startup
	Daemon
	Manual
)

const (
	daemonBackoff    = 30 * time.Second
	daemonMaxCrashes = 5
)

// ParseSchedule classifies a schedule string; cron strings are parsed.
func ParseSchedule(s string) (Kind, cron.Schedule, error) {
	switch s {
	case "@startup":
		return Startup, nil, nil
	case "@daemon":
		return Daemon, nil, nil
	case "@manual":
		return Manual, nil, nil
	}
	sched, err := cron.ParseStandard(s)
	if err != nil {
		return Cron, nil, fmt.Errorf("invalid schedule %q: %v", s, err)
	}
	return Cron, sched, nil
}

// Prev returns the latest scheduled time in (now-window, now], or zero.
func Prev(sched cron.Schedule, now time.Time, window time.Duration) time.Time {
	var last time.Time
	for t := sched.Next(now.Add(-window)); !t.After(now); t = sched.Next(t) {
		last = t
	}
	return last
}

type task struct {
	job   config.Job
	kind  Kind
	sched cron.Schedule
	next  time.Time
	busy  bool // queued or running: never queued twice

	watches []*watched
	daemon  *daemonState
}

type watched struct {
	path      string
	sig       string
	pending   bool
	changedAt time.Time
}

type daemonState struct {
	running bool
	crashes int
	retryAt time.Time
	stop    context.CancelFunc
	done    chan struct{}
}

type request struct {
	t      *task
	reason string
}

// Scheduler runs the configured jobs.
type Scheduler struct {
	cfg   *config.Config
	log   *job.Logger
	out   io.Writer
	tasks []*task

	mu    sync.Mutex
	queue chan request
}

// New validates every job. A job with a bad schedule or no implementation
// is logged, alerted and skipped — it never stops the others.
func New(cfg *config.Config, out io.Writer) *Scheduler {
	s := &Scheduler{cfg: cfg, out: out, log: job.NewLogger("kronos", cfg.Paths["logs"], out)} // kronos.yml: paths.logs
	for _, j := range cfg.Jobs {
		if !j.IsEnabled() { // kronos.yml: jobs.<name>.enabled
			s.log.Info("Skipped %s: disabled", j.Name)
			continue
		}
		kind, sched, err := ParseSchedule(j.Schedule) // kronos.yml: jobs.<name>.schedule
		if err == nil && len(j.Command) == 0 {        // kronos.yml: jobs.<name>.command
			if _, ok := job.Lookup(j.Name); !ok {
				err = fmt.Errorf("no Go job is registered as %q and it has no command:", j.Name)
			}
		}
		if err != nil {
			s.fail(j.Name, fmt.Sprintf("%v – job skipped", err))
			continue
		}
		t := &task{job: j, kind: kind, sched: sched}
		for _, p := range j.Watch { // kronos.yml: jobs.<name>.watch
			t.watches = append(t.watches, &watched{path: p, sig: signature(p)})
		}
		if kind == Daemon {
			t.daemon = &daemonState{}
		}
		s.tasks = append(s.tasks, t)
		s.log.Info("Loaded %s: %s", j.Name, j.Description)
	}
	s.queue = make(chan request, len(s.tasks)*4+8)
	return s
}

// Run schedules jobs until ctx is cancelled, then lets the running job
// finish and stops daemons.
func (s *Scheduler) Run(ctx context.Context) {
	s.log.Info("Started")
	now := time.Now()

	for _, t := range s.tasks {
		if t.kind == Startup {
			s.enqueue(t, "")
		}
	}
	s.catchUp(now)
	for _, t := range s.tasks {
		if t.kind == Cron && !t.busy {
			t.next = t.sched.Next(now)
		}
	}

	workerDone := make(chan struct{})
	go s.worker(ctx, workerDone)

	tick := time.NewTicker(time.Duration(s.cfg.Settings.TickSeconds) * time.Second) // kronos.yml: settings.tick_seconds
	defer tick.Stop()
	s.superviseDaemons(ctx)
	for {
		select {
		case <-ctx.Done():
			s.log.Info("Stopping")
			<-workerDone
			s.stopDaemons()
			s.log.Info("Stopped")
			return
		case now := <-tick.C:
			s.enqueueDue(now)
			s.checkWatches(now)
			s.superviseDaemons(ctx)
		}
	}
}

// catchUp queues cron jobs whose last slot was missed while Kronos was down.
func (s *Scheduler) catchUp(now time.Time) {
	st, err := state.Load(s.cfg.Paths["state"]) // kronos.yml: paths.state
	if err != nil {
		s.log.Warn("%v", err)
	}
	window := time.Duration(s.cfg.Settings.CatchUpHours) * time.Hour // kronos.yml: settings.catch_up_hours

	type missed struct {
		t    *task
		slot time.Time
	}
	var list []missed
	for _, t := range s.tasks {
		if t.kind != Cron || !t.job.CatchesUp() { // kronos.yml: jobs.<name>.catch_up
			continue
		}
		last, ran := st.Jobs[t.job.Name]
		if !ran {
			continue // never ran under Kronos: nothing to catch up
		}
		if slot := Prev(t.sched, now, window); !slot.IsZero() && last.LastRun.Before(slot) {
			list = append(list, missed{t, slot})
		}
	}
	sort.SliceStable(list, func(i, k int) bool { return list[i].slot.Before(list[k].slot) })
	for _, m := range list {
		s.enqueue(m.t, "catch-up for "+m.slot.Format("2006-01-02 15:04"))
	}
}

func (s *Scheduler) enqueueDue(now time.Time) {
	for _, t := range s.tasks {
		if t.kind == Cron && !t.next.IsZero() && !now.Before(t.next) {
			s.enqueue(t, "")
		}
	}
}

func (s *Scheduler) enqueue(t *task, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.busy {
		return
	}
	t.busy = true
	t.next = time.Time{}
	s.queue <- request{t, reason}
}

// worker runs queued jobs one at a time.
func (s *Scheduler) worker(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-s.queue:
			s.execute(req.t, req.reason)
			s.mu.Lock()
			req.t.busy = false
			if req.t.kind == Cron {
				req.t.next = req.t.sched.Next(time.Now())
			}
			for _, w := range req.t.watches {
				w.sig, w.pending = signature(w.path), false // ignore the job's own writes
			}
			s.mu.Unlock()
		}
	}
}

func (s *Scheduler) execute(t *task, reason string) {
	logger := job.NewLogger(t.job.Name, s.cfg.Paths["logs"], s.out)
	desc := t.job.Description
	if reason != "" {
		desc = fmt.Sprintf("%s (%s)", desc, reason)
	}
	logger.Start("%s", desc)

	started := time.Now()
	err := Execute(s.cfg, t.job, false, s.out)
	if recErr := state.Record(s.cfg.Paths["state"], t.job.Name, started, time.Since(started), err); recErr != nil {
		s.log.Warn("State not saved: %v", recErr)
	}
	if err != nil {
		s.fail(t.job.Name, err.Error())
		return
	}
	logger.OK("Completed")
}

func (s *Scheduler) fail(name, msg string) {
	job.NewLogger(name, s.cfg.Paths["logs"], s.out).Fail("%s", msg)
	Alert(s.cfg, s.log, name, msg)
}

// ── Watches ─────────────────────────────────────────────────────────────

func (s *Scheduler) checkWatches(now time.Time) {
	for _, t := range s.tasks {
		for _, w := range t.watches {
			s.mu.Lock()
			if t.busy {
				s.mu.Unlock()
				continue
			}
			sig := signature(w.path)
			fire := false
			switch {
			case sig != w.sig:
				w.sig, w.pending, w.changedAt = sig, true, now
			case w.pending && now.Sub(w.changedAt) >= time.Duration(s.cfg.Settings.TickSeconds)*time.Second:
				w.pending, fire = false, true
			}
			s.mu.Unlock()
			if fire {
				s.enqueue(t, "changed: "+filepath.Base(w.path))
				break
			}
		}
	}
}

// signature identifies a file version by size and modification time.
func signature(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "missing"
	}
	return fmt.Sprintf("%d/%d", info.Size(), info.ModTime().UnixNano())
}

// ── Daemons ─────────────────────────────────────────────────────────────

func (s *Scheduler) superviseDaemons(ctx context.Context) {
	for _, t := range s.tasks {
		d := t.daemon
		if d == nil {
			continue
		}
		s.mu.Lock()
		start := !d.running && d.crashes < daemonMaxCrashes && !time.Now().Before(d.retryAt) && ctx.Err() == nil
		if start {
			d.running = true
		}
		s.mu.Unlock()
		if start {
			s.startDaemon(ctx, t)
		}
	}
}

func (s *Scheduler) startDaemon(parent context.Context, t *task) {
	d := t.daemon
	ctx, cancel := context.WithCancel(parent)
	d.stop, d.done = cancel, make(chan struct{})
	logger := job.NewLogger(t.job.Name, s.cfg.Paths["logs"], s.out)
	logger.Daemon("Started")

	go func() {
		defer close(d.done)
		err := runDaemon(ctx, s.cfg, t.job, s.out)
		if ctx.Err() != nil {
			return // stopped on purpose
		}
		s.mu.Lock()
		d.running = false
		d.crashes++
		d.retryAt = time.Now().Add(daemonBackoff)
		crashes := d.crashes
		s.mu.Unlock()
		reason := "exited"
		if err != nil {
			reason = err.Error()
		}
		if crashes >= daemonMaxCrashes {
			s.fail(t.job.Name, fmt.Sprintf("Crashed %d times – giving up (%s)", crashes, reason))
			return
		}
		logger.Warn("Crashed (%d/%d): %s – restarting in %s", crashes, daemonMaxCrashes, reason, daemonBackoff)
	}()
}

// runDaemon runs a daemon until it exits or ctx is cancelled.
func runDaemon(ctx context.Context, cfg *config.Config, j config.Job, out io.Writer) (err error) {
	if len(j.Command) > 0 {
		return runCommand(ctx, cfg, j, false, out, 0)
	}
	fn, ok := job.Lookup(j.Name)
	if !ok {
		return fmt.Errorf("no Go job is registered as %q", j.Name)
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("crashed: %v", r)
		}
	}()
	return fn(job.NewCtx(ctx, cfg, j, false, out))
}

func (s *Scheduler) stopDaemons() {
	for _, t := range s.tasks {
		d := t.daemon
		if d == nil || d.stop == nil {
			continue
		}
		d.stop()
		select {
		case <-d.done:
			job.NewLogger(t.job.Name, s.cfg.Paths["logs"], s.out).Info("Stopped cleanly")
		case <-time.After(30 * time.Second):
			job.NewLogger(t.job.Name, s.cfg.Paths["logs"], s.out).Warn("Did not stop within 30s")
		}
	}
}
