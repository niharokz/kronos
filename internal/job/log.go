package job

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// logMu serialises log lines from the scheduler, jobs and daemons.
var logMu sync.Mutex

// Logger writes Taskmaster-style lines,
//
//	[2026-10-06 02:30:01] [ OK  ] [event_update] Saved 42 events
//
// to the output (container stdout → docker logs) and, when dir is set, to
// <dir>/kronos_YYYYMM.log.
type Logger struct {
	task string
	dir  string
	out  io.Writer
}

// NewLogger returns a logger for one job (or "kronos" for the scheduler).
func NewLogger(task, dir string, out io.Writer) *Logger {
	if out == nil {
		out = os.Stdout
	}
	return &Logger{task: task, dir: dir, out: out}
}

// Levels, same names as Taskmaster.
func (l *Logger) Start(f string, a ...any)  { l.Write("START", f, a...) }
func (l *Logger) Info(f string, a ...any)   { l.Write("INFO", f, a...) }
func (l *Logger) OK(f string, a ...any)     { l.Write("OK", f, a...) }
func (l *Logger) Skip(f string, a ...any)   { l.Write("SKIP", f, a...) }
func (l *Logger) Warn(f string, a ...any)   { l.Write("WARN", f, a...) }
func (l *Logger) Fail(f string, a ...any)   { l.Write("FAIL", f, a...) }
func (l *Logger) Daemon(f string, a ...any) { l.Write("DAEMN", f, a...) }

// Write logs one line at the given level.
func (l *Logger) Write(level, format string, args ...any) {
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	now := time.Now()
	line := fmt.Sprintf("[%s] [%s] [%s] %s", now.Format("2006-01-02 15:04:05"), center(level, 5), l.task, msg)

	logMu.Lock()
	defer logMu.Unlock()
	fmt.Fprintln(l.out, line)
	if l.dir == "" {
		return
	}
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		fmt.Fprintf(l.out, "log: cannot create %s: %v\n", l.dir, err)
		return
	}
	f, err := os.OpenFile(filepath.Join(l.dir, "kronos_"+now.Format("200601")+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(l.out, "log: %v\n", err)
		return
	}
	defer f.Close()
	fmt.Fprintln(f, line)
}

// center pads like Python's f"{s:^5}" (extra space goes to the right).
func center(s string, width int) string {
	pad := width - len(s)
	if pad <= 0 {
		return s
	}
	left := pad / 2
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", pad-left)
}
