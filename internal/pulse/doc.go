// Package pulse is Kronos's digital + health coach over the phone's daily logs (Kshan; RahiOS before) (module "pulse").
//
// Rules (pure; read files, return values):
//
//	day.go       one daily log → Day (null = missing, never 0)
//	goals.go     goals.md: limits, tracked apps, quiet hours, message cap
//	baseline.go  baseline.md: medians, goals, strikes, targets
//	intraday.go  state/intraday-*.yml snapshots and the step forecast
//	messages.go  message types, priority, cap/quiet planning, inbox files, read feedback
//	rollup.go    weekly/monthly rollups and their plain-numbers text
//	brief.go     state/brief.yml, appended to the morning briefing by nimo_briefing
//	files.go     YAML encoding and loading a range of logs
//
// Jobs (registered here; the only files that write):
//
//	job.go         shared: goals default, deliver (inbox / Telegram / dry-run print), sandbox-aware writes
//	job_watch.go   pulse_watch  – intraday snapshot + app/step nudges
//	job_rollup.go  pulse_rollup – final days → baselines, goals, warnings, rollups, brief
//
// fixture/ writes fake Rahi logs for testing (kronos pulse-fixture).
// Folders (kronos.yml paths, see dirs.go): pulse (own files), daily_logs (+ daily_logs_old, RahiOS,
// read only), phone_inbox (+ phone_inbox_old for read feedback). Kronos never writes to the daily logs.
package pulse
