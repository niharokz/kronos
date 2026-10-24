// Command kronos is Nimory's scheduler: it runs the jobs listed in
// kronos.yml on their schedules (`kronos serve`), and lets you run, list
// and check them from a shell.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
	_ "time/tzdata" // bundled zone data, so .env TZ works in a minimal image

	"gitlab.com/niharokz/kronos/internal/config"
	"gitlab.com/niharokz/kronos/internal/job"
	_ "gitlab.com/niharokz/kronos/internal/jobs" // registers the built-in jobs
	"gitlab.com/niharokz/kronos/internal/outbox"
	"gitlab.com/niharokz/kronos/internal/scheduler"
	"gitlab.com/niharokz/kronos/internal/state"
)

const usage = `kronos — Nimory's job scheduler

Usage:
  kronos serve                       run the scheduler (the container's main process)
  kronos run <job> [--dry-run]       run one job now; --dry-run prints what would change
             [--now 'YYYY-MM-DD HH:MM']   pretend it is this time (needs --dry-run or --rahi-path)
             [--rahi-path DIR]            Pulse jobs only: use a test folder instead of paths.rahi
  kronos pulse-fixture --out DIR [...]   write fake Rahi logs for testing Pulse (--help for options)
  kronos list                        jobs, schedules, next and last runs
  kronos next [n]                    the next n scheduled runs (default 10)
  kronos health                      PASS/FAIL per job; exit 1 if any job failed or missed a run
  kronos jobs                        built-in Go jobs available to kronos.yml
  kronos notify [--kind K] [--title T] "text"
                                     send a Telegram message via Omnimo (text may come from stdin)
  kronos version

Config: $KRONOS_CONFIG or /app/kronos.yml`

var version = "dev" // set at build time: -ldflags "-X main.version=..."

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]

	switch cmd {
	case "version", "--version":
		fmt.Println("kronos", version)
		return
	case "jobs":
		for _, n := range job.Names() {
			fmt.Println(n)
		}
		return
	case "help", "-h", "--help":
		fmt.Println(usage)
		return
	case "pulse-fixture": // no kronos.yml needed
		os.Exit(pulseFixture(args))
	}

	cfg, err := config.Load(config.Path())
	if err != nil {
		fatal(err)
	}

	switch cmd {
	case "serve":
		serve(cfg)
	case "run":
		os.Exit(runJob(cfg, args))
	case "list":
		list(cfg, os.Stdout)
	case "next":
		next(cfg, args, os.Stdout)
	case "health":
		os.Exit(health(cfg, os.Stdout))
	case "notify":
		notify(cfg, args)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "kronos:", err)
	os.Exit(1)
}

func serve(cfg *config.Config) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	scheduler.New(cfg, os.Stdout).Run(ctx)
}

// runJob runs one job now, logs like a scheduled run, and records its state.
func runJob(cfg *config.Config, args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	dry := fs.Bool("dry-run", false, "print what would change without changing anything")
	nowFlag := fs.String("now", "", "pretend it is this local time: 'YYYY-MM-DD HH:MM'")
	rahi := fs.String("rahi-path", "", "Pulse test folder used instead of paths.rahi")
	name, rest := "", args
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		name, rest = rest[0], rest[1:]
	}
	_ = fs.Parse(rest)
	if name == "" && fs.NArg() > 0 {
		name = fs.Arg(0)
	}
	if name == "" {
		fmt.Fprintln(os.Stderr, "Usage: kronos run <job> [--dry-run] [--now 'YYYY-MM-DD HH:MM'] [--rahi-path DIR]")
		return 2
	}
	opt, err := testOptions(cfg, name, *dry, *nowFlag, *rahi)
	if err != nil {
		fmt.Fprintln(os.Stderr, "kronos:", err)
		return 2
	}
	test := !opt.Now.IsZero() || opt.Sandbox // never touches the real log or state

	j, ok := cfg.Job(name)
	if !ok {
		if _, registered := job.Lookup(name); !registered {
			fmt.Fprintf(os.Stderr, "kronos: unknown job %q (see `kronos list` and `kronos jobs`)\n", name)
			return 2
		}
		j = config.Job{Name: name, Description: "(not in kronos.yml)"}
	}

	logDir := cfg.Paths["logs"] // kronos.yml: paths.logs
	switch {
	case opt.Sandbox:
		logDir = ""
		fmt.Printf("Test run of %s in %s (Telegram → %s)\n", name, *rahi, cfg.Paths["outbox"])
	case *dry:
		logDir = ""
		fmt.Printf("Dry run of %s — nothing will be changed.\n", name)
	}
	if !opt.Now.IsZero() {
		fmt.Printf("Clock set to %s\n", opt.Now.Format("Mon 2006-01-02 15:04"))
	}
	logger := job.NewLogger(name, logDir, os.Stdout)
	logger.Start("%s (manual)", j.Description)

	started := time.Now()
	err = scheduler.ExecuteWith(cfg, j, opt, os.Stdout)
	if !*dry && !test {
		if recErr := state.Record(cfg.Paths["state"], name, started, time.Since(started), err); recErr != nil {
			logger.Warn("State not saved: %v", recErr)
		}
	}
	if err != nil {
		logger.Fail("%v", err)
		return 1
	}
	logger.OK("Completed")
	return 0
}

// testOptions turns the test flags into run options. --rahi-path points paths.rahi at a
// sandbox and moves the outbox and logs there too, so nothing reaches Telegram or your logs.
func testOptions(cfg *config.Config, name string, dry bool, now, rahi string) (scheduler.Options, error) {
	o := scheduler.Options{DryRun: dry}
	if rahi != "" {
		if !strings.HasPrefix(name, "pulse_") {
			return o, fmt.Errorf("--rahi-path is for the pulse_* jobs (other jobs would still touch real paths)")
		}
		abs, err := filepath.Abs(rahi)
		if err != nil {
			return o, err
		}
		cfg.Paths["rahi"] = abs
		cfg.Paths["outbox"] = filepath.Join(abs, "_outbox")
		cfg.Paths["logs"] = ""
		o.Sandbox = true
	}
	if now != "" {
		t, err := time.ParseInLocation("2006-01-02 15:04", now, time.Local)
		if err != nil {
			return o, fmt.Errorf("--now %q: want 'YYYY-MM-DD HH:MM'", now)
		}
		if !dry && !o.Sandbox {
			return o, fmt.Errorf("--now on real data needs --dry-run (or use --rahi-path)")
		}
		o.Now = t
	}
	return o, nil
}

func list(cfg *config.Config, out io.Writer) {
	st, _ := state.Load(cfg.Paths["state"]) // kronos.yml: paths.state
	now := time.Now()
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "JOB\tSCHEDULE\tNEXT\tLAST RUN\tSTATUS\tDESCRIPTION")
	for _, j := range cfg.Jobs {
		nextRun := "-"
		kind, sched, err := scheduler.ParseSchedule(j.Schedule)
		switch {
		case !j.IsEnabled():
			nextRun = "disabled"
		case err != nil:
			nextRun = "INVALID"
		case kind == scheduler.Cron:
			nextRun = sched.Next(now).Format("Mon 02 Jan 15:04")
		}
		if len(j.Watch) > 0 {
			nextRun += " +watch"
		}
		last, status := "-", "-"
		if s, ok := st.Jobs[j.Name]; ok {
			last, status = s.LastRun.Format("2006-01-02 15:04"), s.LastStatus
		}
		if _, ok := job.Lookup(j.Name); !ok && len(j.Command) == 0 {
			status = "NO JOB"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", j.Name, j.Schedule, nextRun, last, status, j.Description)
	}
	w.Flush()
}

func next(cfg *config.Config, args []string, out io.Writer) {
	n := 10
	if len(args) > 0 {
		fmt.Sscan(args[0], &n)
	}
	type run struct {
		at   time.Time
		name string
	}
	var runs []run
	now := time.Now()
	for _, j := range cfg.Jobs {
		kind, sched, err := scheduler.ParseSchedule(j.Schedule)
		if err != nil || kind != scheduler.Cron || !j.IsEnabled() {
			continue
		}
		t := now
		for i := 0; i < n; i++ {
			t = sched.Next(t)
			runs = append(runs, run{t, j.Name})
		}
	}
	sort.SliceStable(runs, func(a, b int) bool { return runs[a].at.Before(runs[b].at) })
	if len(runs) > n {
		runs = runs[:n]
	}
	for _, r := range runs {
		fmt.Fprintf(out, "%s  %s\n", r.at.Format("Mon 02 Jan 15:04"), r.name)
	}
}

// health reports each scheduled job: FAIL if its last run failed or a
// scheduled slot (older than 15 minutes) was missed. Exit 1 on any FAIL.
func health(cfg *config.Config, out io.Writer) int {
	st, err := state.Load(cfg.Paths["state"]) // kronos.yml: paths.state
	if err != nil {
		fmt.Fprintf(out, "FAIL  state: %v\n", err)
		return 1
	}
	now := time.Now()
	window := time.Duration(cfg.Settings.CatchUpHours) * time.Hour // kronos.yml: settings.catch_up_hours
	if window < 8*24*time.Hour {
		window = 8 * 24 * time.Hour // long enough to see a missed weekly job
	}
	failed := false
	for _, j := range cfg.Jobs {
		kind, sched, err := scheduler.ParseSchedule(j.Schedule)
		if !j.IsEnabled() || kind == scheduler.Daemon || kind == scheduler.Manual {
			continue
		}
		if err != nil {
			fmt.Fprintf(out, "FAIL  %-22s %v\n", j.Name, err)
			failed = true
			continue
		}
		s, ran := st.Jobs[j.Name]
		switch {
		case !ran:
			fmt.Fprintf(out, "WAIT  %-22s not run yet\n", j.Name)
		case s.LastStatus == state.Fail:
			fmt.Fprintf(out, "FAIL  %-22s last run %s failed: %s\n", j.Name, s.LastRun.Format("2006-01-02 15:04"), firstLine(s.LastError))
			failed = true
		case kind == scheduler.Cron:
			if slot := scheduler.Prev(sched, now.Add(-15*time.Minute), window); !slot.IsZero() && s.LastRun.Before(slot) {
				fmt.Fprintf(out, "FAIL  %-22s missed run at %s (last %s)\n", j.Name, slot.Format("2006-01-02 15:04"), s.LastRun.Format("2006-01-02 15:04"))
				failed = true
				continue
			}
			fallthrough
		default:
			fmt.Fprintf(out, "PASS  %-22s last ok %s (%.1fs)\n", j.Name, s.LastOK.Format("2006-01-02 15:04"), float64(s.DurationMs)/1000)
		}
	}
	if failed {
		return 1
	}
	return 0
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func notify(cfg *config.Config, args []string) {
	fs := flag.NewFlagSet("notify", flag.ExitOnError)
	kind := fs.String("kind", outbox.Notice, "notice | alert")
	title := fs.String("title", "", "short label (shown in alerts)")
	_ = fs.Parse(args)

	text := strings.Join(fs.Args(), " ")
	if text == "" {
		raw, _ := io.ReadAll(os.Stdin)
		text = string(raw)
	}
	path, err := outbox.Write(cfg.Paths["outbox"], job.Source, *kind, *title, text) // kronos.yml: paths.outbox
	if err != nil {
		fatal(err)
	}
	fmt.Println("queued", path)
}
