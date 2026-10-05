// Package jobs holds Kronos's built-in jobs, one file per job.
//
// Each file registers itself in an init() function:
//
//	func init() { job.Register("event_update", eventUpdate) }
//
// and is switched on by an entry with the same name in kronos.yml. To add a
// job, copy any small file here (archive_rollover.go is the simplest), give
// it a new name, add it to kronos.yml, and rebuild.
//
// The jobs are a 1:1 port of Taskmaster's Python tasks: same names, same
// rules, same order of steps, same log messages. The only intended
// behaviour differences are listed in ARCHITECTURE.md ("Differences from
// Taskmaster").
package jobs
