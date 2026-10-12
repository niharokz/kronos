// Package jobs holds Kronos's built-in jobs. Each job is self-contained:
//
//	<name>.go       the job; registers itself: func init() { job.Register("<name>", fn) }
//	<name>_test.go  its tests (shared test helpers are in jobs_test.go)
//
// plus one entry under jobs: in kronos.yml.example (and your kronos.yml).
//
// Add a job: copy a small job (archive_rollover.go), rename it, add its
// kronos.yml.example entry, rebuild. New paths go in config.defaultPaths and
// the example's paths:.
//
// Remove a job: delete <name>.go and <name>_test.go, delete its entries in
// kronos.yml.example and kronos.yml, rebuild.
//
// registry_test.go fails if the registered jobs and kronos.yml.example
// disagree, so a half-removed job is caught by `go test`.
//
// Code shared by several jobs lives in helpers.go; nothing job-specific goes there.
package jobs
