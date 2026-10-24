package scheduler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"gitlab.com/niharokz/kronos/internal/config"
	"gitlab.com/niharokz/kronos/internal/dates"
	"gitlab.com/niharokz/kronos/internal/job"
	"gitlab.com/niharokz/kronos/internal/outbox"
)

// Execute runs one job to completion and returns its error. It is used by
// the scheduler and by `kronos run`.
//
// Go jobs run in-process with panic recovery; command jobs run as a child
// process. Either way the run is bounded by the job's timeout. Shutting
// Kronos down does not interrupt a running job (it finishes; compose
// gives it stop_grace_period), so a data file is never left half-done.
func Execute(cfg *config.Config, j config.Job, dryRun bool, out io.Writer) error {
	return ExecuteWith(cfg, j, Options{DryRun: dryRun}, out)
}

// Options are manual-run overrides (kronos run): dry run, a fake clock, sandbox mode.
type Options struct {
	DryRun  bool
	Now     time.Time // zero = real time
	Sandbox bool      // paths point at a test folder (see job.Ctx.Sandbox)
}

// ExecuteWith is Execute with manual-run overrides.
func ExecuteWith(cfg *config.Config, j config.Job, o Options, out io.Writer) error {
	dryRun := o.DryRun
	timeout := time.Duration(j.Timeout) * time.Second
	if timeout <= 0 {
		timeout = time.Duration(cfg.Settings.TimeoutSeconds) * time.Second // kronos.yml: settings.timeout_seconds (per job: jobs.<name>.timeout)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if len(j.Command) > 0 {
		return runCommand(ctx, cfg, j, dryRun, out, timeout)
	}

	fn, ok := job.Lookup(j.Name)
	if !ok {
		return fmt.Errorf("no Go job is registered as %q and it has no command: — check the name in %s", j.Name, cfg.File)
	}

	c := job.NewCtx(ctx, cfg, j, dryRun, out)
	if !o.Now.IsZero() { // --now: test time-of-day rules
		c.Now, c.Today = o.Now, dates.Today(o.Now)
	}
	c.Sandbox = o.Sandbox
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(out, "panic in job %s: %v\n%s", j.Name, r, debug.Stack())
				done <- fmt.Errorf("crashed: %v", r)
			}
		}()
		done <- fn(c)
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("Timed out after %s – abandoned (it may still finish in the background)", timeout)
	}
}

// runCommand runs a command job. The child gets the container's
// environment plus KRONOS_<PATH> for every paths: entry, KRONOS_JOB and
// KRONOS_DRY_RUN. A non-zero exit fails the job with the last lines of its
// stderr in the message.
func runCommand(ctx context.Context, cfg *config.Config, j config.Job, dryRun bool, out io.Writer, timeout time.Duration) error {
	cmd := exec.CommandContext(ctx, j.Command[0], j.Command[1:]...)
	cmd.Env = append(os.Environ(), commandEnv(cfg, j, dryRun)...)
	var stderr bytes.Buffer
	cmd.Stdout = out
	cmd.Stderr = io.MultiWriter(out, &stderr)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second

	err := cmd.Run()
	if err == nil {
		return nil
	}
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("Timed out after %s – killed", timeout)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		msg := fmt.Sprintf("Exited with code %d", exitErr.ExitCode())
		if tail := lastLines(stderr.String(), 5); tail != "" {
			msg += "\n" + tail
		}
		return errors.New(msg)
	}
	return fmt.Errorf("cannot run %s: %w", j.Command[0], err)
}

func commandEnv(cfg *config.Config, j config.Job, dryRun bool) []string {
	env := []string{
		"KRONOS_JOB=" + j.Name,
		"KRONOS_CONFIG=" + cfg.File,
	}
	if dryRun {
		env = append(env, "KRONOS_DRY_RUN=1")
	} else {
		env = append(env, "KRONOS_DRY_RUN=0")
	}
	for k, v := range cfg.Paths { // kronos.yml: paths.* → KRONOS_<NAME> for scripts
		env = append(env, "KRONOS_"+strings.ToUpper(k)+"="+v)
	}
	return env
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// Alert sends a failure to Telegram via Omnimo (best effort).
func Alert(cfg *config.Config, log *job.Logger, title, text string) {
	if !cfg.Settings.Alerts { // kronos.yml: settings.alerts
		return
	}
	if _, err := outbox.Write(cfg.Paths["outbox"], job.Source, outbox.Alert, title, text); err != nil { // kronos.yml: paths.outbox
		log.Warn("Alert not sent: %v", err)
	}
}
