# Kronos — Architecture (internal)

> Complete reference for the code: every file, job, data contract and rule. Share this file (plus only the files being changed) instead of the full source.
> Contains no secret values and no host paths. Last reviewed: 2026-11-12 (v1.2.2: Kshan module, phone paths, run_at_start).

---

## 1. Context

Kronos is the time-driven half of the Nimory homelab: nightly maintenance of the notes vault's data files, the agenda, the health site, the morning briefing, the Pulse coach and a few web monitors. It replaced **Taskmaster** (Python) with a 1:1 port in Go — same job names, rules, order and log messages (see §11).

| Project | Role | Shares with Kronos |
|---|---|---|
| **Omnimo** (Go) | Telegram bot + AI (Nimo) — the only Telegram sender | `event.md`, the **outbox** folder |
| **Natlas** (Go) | Web UI for the data files | `event.md`, `subscription.md`, `health.md` |
| **Arthik** | Finance app | `data/finance/inbox/kronos.md` (Kronos writes, Arthik reads) |
| **Kshan** (Android) | Widget app (replaced RahiOS) — writes daily logs and event drafts, shows the inbox and weather | `logs/kshan/` (Kshan writes `daily/`, `events.md`, `config.md`, `weather/location.md`; Kronos writes `weather/weather.md`, `inbox/`) |
| **Kronos** (Go) | Scheduler | — |

Kronos never talks to Telegram or any AI. It writes outbox files; Omnimo sends them.

Host: a small Debian box with Docker Compose. Repo: `gitlab.com/niharokz/kronos`, deployed from a clone. Every host-specific value (folders, UID, time zone, the health-site folder) is in the gitignored `.env`; `kronos.yml` uses container paths only.

---

## 2. Modules

Kronos is a small core plus four job modules. A module is one Go package that registers its own jobs in `init()`; `cmd/kronos/main.go` imports each one (`_ "…/internal/<module>"`). Modules never import each other, except `jobs → pulse` (the briefing appends Pulse's brief).

| Module | Package | Jobs | Data | External calls |
|---|---|---|---|---|
| **Vault jobs** | `internal/jobs` | `restarted`, `untitled_import`, `inbox_archive`, `event_archive`, `subscription_update`, `arthik_subscriptions`, `event_update`, `calendar_sync`, `health_website`, `daily_update`, `archive_rollover`, `nimo_briefing` | `/data/notes/data`, `inbox`, `archive`, health site | Google Calendar API only |
| **Pulse** | `internal/pulse` | `pulse_watch`, `pulse_rollup` | `logs/pulse` (own), reads `logs/kshan/daily` (+ `logs/rahi/daily`), writes `logs/kshan/inbox` | none |
| **Kshan** | `internal/kshan` | `kshan_weather`, `kshan_events` | `logs/kshan/weather/weather.md` (reads `weather/location.md`), reads `logs/kshan/events.md` (+ `logs/rahi/events.md`) → `data/event.md` | api.open-meteo.com |
| **Monitors** | `internal/watch` | `watch_rewards`, `watch_gold`, `watch_domains` | `/data/notes/logs/watch` | the watched sites |

Core (shared by all modules): `config`, `scheduler`, `job`, `store`, `dates`, `outbox`, `state`.

Remove a module: delete its folder, its import in `main.go` (and `registry_test.go`), its jobs in `kronos.yml.example`/`kronos.yml`. Nothing else refers to it.

---

## 3. Files

```
.env.example                 template for .env: NOTE_PATH, KRONOS_HEALTH_SITE, PUID, PGID, TZ (no secrets)
docker-compose.yml           one service; mounts, hardening, version; all host values from .env
Dockerfile                   golang:1.23-alpine build (vet + test + build) → alpine:3.20 runtime, USER 1000
kronos.yml.example           every job with its schedule plan and options (copy to kronos.yml, gitignored)
.gitlab-ci.yml               gofmt, go mod verify, vet, test, build
.gitignore / .dockerignore   secrets, personal config, patches, build output, editor clutter
.gitattributes               LF line endings in the repo
LICENSE                      MIT
README.md                    public user guide
ARCHITECTURE.md              this file
config/.gitkeep              ./config is mounted at /app/config (token.json, credentials.json — gitignored)
scripts/disk_check.sh.example  example script job (command:)

cmd/kronos/
  main.go                    CLI: serve | run <job> [--dry-run] [--now] [--sandbox] | list | next [n] | health | jobs | notify | version
  fixture.go                 CLI: pulse-fixture (flags → pulse/fixture.Options)
  main_test.go               test flags (--now/--sandbox rules), fixture flags

internal/config/
  config.go                  kronos.yml → Config{Paths, Settings, Jobs (ordered)}; defaultPaths; unknown keys rejected;
                             Job{Name, Schedule, Description, Enabled, Timeout, CatchUp, SkipIfBusy, RunAtStart, Watch, Command, Options};
                             Job.Int/String/Bool/Strings (options)
  config_test.go             parsing, defaults, unknown keys

internal/scheduler/
  scheduler.go               kinds (cron/@startup/@daemon/@manual), queue + one worker, catch-up, skip_if_busy, run_at_start,
                             file watches, daemons, shutdown; ParseSchedule, Prev, New, Run
  run.go                     Execute/ExecuteWith (Options{DryRun, Now, Sandbox}): timeout, panic recovery,
                             command jobs (env, exit code, stderr tail); Alert (FAIL → outbox alert)
  scheduler_test.go          outcomes, command env, bad jobs, catch-up order, Prev, watch debounce, skip_if_busy, run_at_start

internal/job/
  job.go                     Register/Lookup/Names; Ctx{Now, Today, DryRun, Sandbox, Cfg, Job, Log, Out}:
                             Path/OptionalPath/VaultFile, options, Notify, WriteFile/AppendFile/Move/Remove/MkdirAll (dry-run aware)
  log.go                     Logger: "[ts] [LEVEL] [job] msg" to stdout + logs/kronos/kronos_YYYYMM.log
  diff.go                    unified-style diff printed by dry runs

internal/store/
  yaml.go                    records engine on yaml.Node: LoadMapping/ParseMapping/LoadRecords/RecordList/RecordsYAML/Encode,
                             Get/Set/Delete/Has, Str/Date/Quoted/Seq/Int, Field/Scalar/IsNull, DateOf/IntOf/Equals/Truthy
  pystr.go                   Python-equal text: PyStr/PyRepr/PyFloat/PyHTMLEscape
  files.go                   AtomicWrite(Perm) (tmp + fsync + rename), UniquePath (name_1.md …)
  store_test.go

internal/dates/
  dates.go                   Today/Format/ParseISO/ParseTimestamp, AddMonths/AddYears (anchor), Rule (ParseRule/Step),
                             Advance/RollForward, AnchorDay
  dates_test.go

internal/outbox/
  outbox.go                  Write(dir, source, kind, title, text): producer side of Omnimo's outbox contract (§8)
  outbox_test.go

internal/state/
  state.go                   state.json: Load, Record(last_run, status, last_ok, last_error, duration)

internal/jobs/               module "vault jobs" — one file per job (+ its _test.go)
  doc.go                     package rules (how to add/remove a job)
  helpers.go                 shared by event/subscription jobs: pyGet, isOneTime/isActive, rule anchors, slugify, file checks
  restarted.go               @startup notice: uptime, RAM
  untitled_import.go         inbox/*untitled* → note.md
  inbox_archive.go           old inbox files → archive/
  event_archive.go           closed one-time events → event.md.bck
  subscription_update.go     roll due dates, due-today → pending event
  arthik_subscriptions.go    due subscriptions → Arthik drafts (kronos.md)
  event_update.go            overdue/recurring/purge/sort of event.md
  calendar_sync.go           Google Calendar → calendar.md (OAuth refresh, categories)
  health_website.go          health.md → health site index.html (nss theme)
  health_daily.go            the four daily-activity SVG cards of the health site
  assets/health.css          health site stylesheet (embedded)
  daily_update.go            agenda → inbox/daily.md
  archive_rollover.go        archive files → archive/<year-1>/ (1 Jan)
  nimo_briefing.go           daily.md (+ Pulse brief) → outbox briefing
  jobs_test.go               shared test helpers (testCtx, put, eventByID)
  registry_test.go           registered jobs (all modules) == jobs in kronos.yml.example

internal/pulse/              module "pulse" (§6.8)
  doc.go                     file roles
  day.go                     Day, LoadDay/ParseDay, Value (missing vs 0), LogDates, Weekend
  goals.go                   Goals (goals.md), DefaultGoals, Metric, Clamp, quiet hours
  baseline.go                State (baseline.md): Recompute, medians, StartGoals, AdjustWeek, Target, Over/Pattern/Overs
  dirs.go                    Dirs{Home, Daily, DailyOld, Inbox, InboxOld}: Day (new folder, else old), SandboxPaths
  intraday.go                snapshots (state/intraday-YYYYMMDD.yml), StepsStillToCome, MedianClock
  messages.go                types + Priority, Sent (state/sent.yml), Plan (cap/quiet/defer), inbox files, LoadRead, BackedOff
  rollup.go                  BuildRollup (weekly/monthly), ReportText
  brief.go                   Brief (state/brief.yml), BuildBrief, Text
  files.go                   Encode (YAML + header), LoadLogs
  job.go                     shared job code: goals default, deliver (inbox/Telegram/dry-run print), sandbox-aware writes
  job_watch.go               pulse_watch
  job_rollup.go              pulse_rollup
  *_test.go                  rules (pulse_test.go) and jobs (job_*_test.go, helpers in job_helpers_test.go)
  fixture/fixture.go         fake phone logs + snapshots + goals.md for testing (kronos pulse-fixture)
  fixture/fixture_test.go

internal/kshan/              module "kshan" (§6.10)
  doc.go                     file roles
  weather.go                 kshan_weather: location.md → open-meteo → weather.md, skip rule, run reason; failure mark next to state.json
  events.go                  kshan_events: drafts (Kshan + RahiOS fallback) → event.md
  when.go                    ParseWhen: title, date and time from a draft's text
  *_test.go                  golden weather.md (testdata/forecast.json → testdata/weather.golden), location.md parsing, skip rule, failures, dry run,
                             WMO words, draft import/idempotency/errors/fallback, date phrases

internal/watch/              module "monitors" (§6.9)
  watch.go                   Watcher[T], Register, Run: compare/store/notify/failure count/dry run
  client.go                  HTTP client (UA, timeout, retry) + HTML helpers Text/Attr/Number/Commas
  rewards.go                 watch_rewards
  gold.go                    watch_gold
  domains.go                 watch_domains
  *_test.go                  engine + each monitor's page shapes
```

Dependencies: `gopkg.in/yaml.v3` (via its GitHub mirror — `replace` in go.mod), `github.com/robfig/cron/v3`. Google and the monitors use plain `net/http` (no SDKs, no HTML parser).

---

## 4. Deployment

| Item | Value |
|---|---|
| Image | multi-stage: `golang:1.23-alpine` (runs `go vet` + `go test`) → `alpine:3.20` + `ca-certificates`; static binary `/app/kronos` (~7 MB), on `PATH`; `VERSION` build arg → `kronos version` |
| Command | `kronos serve` |
| User / hardening | `.env` `PUID:PGID` (image default `USER 1000:1000`), `init: true`, `read_only: true` + tmpfs `/tmp` (16 MB), `cap_drop: [ALL]`, `no-new-privileges`, `pids_limit: 256`, `mem_limit: 256m`, `stop_grace_period: 1m` |
| TZ | `.env` `TZ` (default UTC); zone data compiled in (`time/tzdata`) |
| Volumes | `$NOTE_PATH → /data/notes` · `$KRONOS_HEALTH_SITE → /data/health-site` · `./kronos.yml → /app/kronos.yml:ro` · `./config → /app/config` (rw: token refresh) · `./scripts → /app/scripts:ro` |
| Network | default bridge, no ports. Outbound: `www.googleapis.com`, `oauth2.googleapis.com` (calendar_sync); `rewards.bing.com`, `cleartax.in`, `domainoffer.net`, `tld-list.com` (monitors, or your `options.url`) |
| `.env` | gitignored; may be a symlink to a shared homelab `.env`. Read only by compose: `NOTE_PATH` (often `${DATA_PATH}/notes`), `KRONOS_HEALTH_SITE`, `PUID`, `PGID`, `TZ`. Only `TZ` enters the container. Template: `.env.example` |
| Secrets | none in env or repo. Google OAuth: `./config/token.json` (0600, refreshed in place) + `credentials.json` (only to regenerate the token) — both gitignored and dockerignored |
| Ignored | `.gitignore`: `.env*` (except `.env.example`), `kronos.yml*` (except `.example`), `config/*`, `scripts/*` (except `*.example`), OAuth files anywhere, `patch/`, `*.zip`, `*.bak`, build output, editor/OS files · `.dockerignore`: the same + `.git`, docs, compose/Dockerfile |
| Logs | stdout (json-file 10 MB × 3) + `/data/notes/logs/kronos/kronos_YYYYMM.log` |
| State | `/data/notes/logs/kronos/state.json` |

Rebuild after code changes (`docker compose up -d --build`); **restart after `kronos.yml` changes** (`docker compose restart kronos` — a rebuild with unchanged code does not recreate the container); nothing for a new script. `.env` changes need `docker compose up -d`.

CI (`.gitlab-ci.yml`): `gofmt -l` empty, `go mod verify`, `go vet`, `go test`, build. Config parsing rejects unknown keys at the top level, under `settings:` and inside a job (`paths:` and `options:` stay free-form).

### Default paths (`config.defaultPaths`; kronos.yml `paths:` overrides)

| Key | Container path | Used by |
|---|---|---|
| `logs` | `/data/notes/logs/kronos` | logger |
| `state` | `/data/notes/logs/kronos/state.json` | scheduler (catch-up, health) |
| `outbox` | `/data/notes/data/outbox` | Notify / alerts (= Omnimo `outbox.dir`) |
| `events`, `event_backup` | `/data/notes/data/event.md`, `.bck` | event jobs, subscription_update, daily_update |
| `subscriptions` | `/data/notes/data/subscription.md` | subscription jobs, daily_update |
| `calendar` | `/data/notes/data/calendar.md` | calendar_sync, daily_update |
| `health` | `/data/notes/data/health.md` | health_website |
| `health_site` | `/data/health-site` | health_website (`.env KRONOS_HEALTH_SITE`) |
| `inbox`, `archive`, `daily` | `/data/notes/inbox`, `/data/notes/archive`, `/data/notes/inbox/daily.md` | inbox jobs, agenda, briefing |
| `finance_inbox` | `/data/notes/data/finance/inbox` | arthik_subscriptions |
| `vault` | `/data/notes` | relative file options (`kshan_weather.location_file`/`weather_file`) |
| `pulse` | `/data/notes/logs/pulse` | Pulse's own files; nimo_briefing (brief) |
| `daily_logs`, `daily_logs_old` | `/data/notes/logs/kshan/daily`, `…/logs/rahi/daily` | Pulse (old = read-only fallback per day; `""` = off) |
| `phone_inbox`, `phone_inbox_old` | `/data/notes/logs/kshan/inbox`, `…/logs/rahi/inbox` | Pulse + monitors write the new one; the old one's `read/` is read for feedback |
| `kshan_events`, `kshan_events_old` | `/data/notes/logs/kshan/events.md`, `…/logs/rahi/events.md` | kshan_events (both read only) |
| `watch` | `/data/notes/logs/watch` | monitors |
| `google_token` | `/app/config/token.json` | calendar_sync |

Every path is also exported to command jobs as `KRONOS_<KEY>`.

---

## 5. Scheduler

- **Kinds**: 5-field cron (robfig `ParseStandard`, local TZ) · `@startup` · `@daemon` · `@manual`. Invalid schedule or unknown job (no Go registration and no `command:`) → `FAIL` + alert at load, job skipped, others run.
- **Queue**: one worker, strictly sequential. `busy` flag = never queued twice. Cron `next` is cleared on enqueue and recomputed from *finish* time (overruns skip slots). Jobs due in the same tick queue in `kronos.yml` order.
- **Startup order**: `@startup` jobs (config order) → catch-ups (slot order) → normal schedule.
- **Run reason**: the scheduler passes why a job runs to `Ctx.Reason` (`""` schedule, `start`, `changed: <file>`, `catch-up for …`; `kronos run` = `manual`).
- **Run at start**: a cron job with `run_at_start: true` is also queued once at start, after the `@startup` jobs and before catch-ups (`kshan_weather`).
- **Skip if busy**: a cron job with `skip_if_busy: true` that comes due while a job runs or waits is not queued: `SKIP Another job is running – this slot skipped`, next = next slot (`watch_rewards`).
- **Catch-up**: cron jobs with `catch_up` (default true) and a state entry: if the latest slot within `catch_up_hours` (48) is after `last_run`, run once (`catch-up for <slot>`). No state entry → no catch-up. A failed run counts as a run.
- **Watch**: every tick, stat watched files (size + mtime). Change → pending; unchanged ≥ one tick → enqueue (`changed: <file>`). After any run of that job its signatures refresh (own writes never re-trigger).
- **Timeouts**: job `timeout` or `settings.timeout_seconds` (900). Go job: `Timed out … abandoned` (goroutine may finish). Command: SIGTERM, kill after 10 s.
- **Panics** in Go jobs → `crashed: <value>` + stack to stdout.
- **Daemons**: own goroutine/process; exit → retry after 30 s, give up after 5 crashes (alert). Shutdown: cancel/SIGTERM, wait 30 s.
- **Shutdown**: SIGTERM → stop queueing → running job finishes → stop daemons → exit 0.
- **Alerts**: every `FAIL` → `outbox.Write(alert, title=<job>, text=<error>)`; `settings.alerts: false` disables. `kronos run` never alerts.
- **State**: after every scheduled or manual non-test run: `last_run, last_status (ok|fail), last_ok, last_error, duration_ms`.
- **Command jobs**: `exec` with container env + `KRONOS_<PATHKEY>` + `KRONOS_JOB`, `KRONOS_CONFIG`, `KRONOS_DRY_RUN=0|1`. stdout/stderr → log. Non-zero exit → `Exited with code N` + last 5 stderr lines.
- **Manual runs** (`kronos run <job>`): `--dry-run` (Ctx.DryRun: file ops print diffs), `--now 'YYYY-MM-DD HH:MM'` (Ctx.Now/Today; real data needs `--dry-run`), `--sandbox DIR` (old name `--rahi-path`; pulse_* only: `paths.pulse`=DIR, `daily_logs`=DIR/daily, `phone_inbox`=DIR/inbox, fallbacks off, `paths.outbox`=DIR/_outbox, no log file, Ctx.Sandbox). Dry and test runs don't record state.

---

## 6. Jobs and schedule

### 6.1 Schedule plan (container time, TZ = Asia/Kolkata on the server)

One worker runs jobs one at a time, so overlaps only queue; the plan keeps every slot free anyway. Lanes: **:00/:30** `pulse_watch` (08–22), **:10** `pulse_rollup`, **:15/:45** `kshan_events`, **:20** `kshan_weather` (+ at start, + on `location.md` change), **:40** `watch_rewards`. Daily jobs use the other minutes, ≥ 10 min apart, in dependency order.

| Time | Job | Why there |
|---|---|---|
| start | `restarted` | — |
| 02:05 | `untitled_import` | before inbox_archive (note.md stays fresh) |
| 02:15 | `inbox_archive` | |
| 02:25 | `event_archive` | backup before event changes |
| 02:35 | `subscription_update` | creates today's `sub-…` events |
| 02:45 | `arthik_subscriptions` | reads those events |
| 02:55 | `event_update` | after both event writers |
| 03:15 | `calendar_sync` | network; before the agenda |
| 03:25 | `health_website` (+ on `health.md` edit) | |
| 03:35 | `daily_update` | after events, subscriptions, calendar |
| 03:45 1 Jan | `archive_rollover` | |
| 05:40 | `nimo_briefing` | after daily_update and the 05:10 `pulse_rollup` (brief) |
| */30 08–22 | `pulse_watch` | |
| :10 hourly | `pulse_rollup` | `briefing_at` = 05:40 |
| :15/:45 | `kshan_events` | drafts show up in event.md within 30 min |
| :20 hourly + at start + on change | `kshan_weather` | network call skipped while fresh (< 50 min) and the location is unchanged |
| :40 hourly, skip if busy | `watch_rewards` | skipped at 05:40 when the briefing runs |
| 11:15 | `watch_gold` | after the day's rate is published |
| 11:35 | `watch_domains` | |

### 6.2 What each job reads and writes

Paths relative to `/data/notes/`.

| Job | Reads | Writes | Options (default) |
|---|---|---|---|
| `restarted` | `/proc/meminfo`, `/proc/uptime` | outbox notice | `label` (Nimory) |
| `untitled_import` | `inbox/*untitled*` | `inbox/note.md` (append), deletes sources | `note_file` (note.md) |
| `inbox_archive` | `inbox/*` by mtime | `archive/` via `UniquePath` | `archive_after_days` (30), `exclude` ([note.md, guide.md]) |
| `event_archive` | `event.md`, `event.md.bck` | backup first, then `event.md` | `archive_after_days` (30) |
| `subscription_update` | `subscription.md`, `event.md` | both | — |
| `arthik_subscriptions` | `subscription.md`, `event.md`, `data/finance/inbox/kronos.md` | `data/finance/inbox/kronos.md` | `max_catch_up_days` (3), `keep_days` (60) |
| `event_update` | `event.md` | `event.md` | `purge_after_days` (365) |
| `calendar_sync` | Calendar API, `config/token.json` | `calendar.md` (+ token on refresh, 0600) | `past_days` (7), `ahead_days` (365), `exclude` ([]), `categories` ({}) |
| `health_website` | `health.md` | `<health_site>/index.html` | — |
| `daily_update` | events, subscriptions, calendar | `inbox/daily.md` | `upcoming_days` (7) |
| `archive_rollover` | `archive/*` | `archive/<year-1>/` | — |
| `nimo_briefing` | `inbox/daily.md`, `logs/pulse/state/brief.yml` | outbox briefing (+ `# Pulse` block) | — |
| `pulse_watch` | today's `logs/kshan/daily/…`, goals, baseline, sent, `logs/kshan/inbox/read` (+ old) | snapshot, phone-inbox nudges, sent | — |
| `pulse_rollup` | final `logs/kshan/daily/…` (+ `logs/rahi/daily` fallback, 70 days), goals | baseline, weekly/monthly, brief, warnings/reports, sent | `briefing_at` (05:40) |
| `kshan_weather` | `logs/kshan/weather/location.md`, api.open-meteo.com | `logs/kshan/weather/weather.md` | `location_file`, `weather_file`, `hours` (6), `every` (60), fallback `lat` (12.925) / `lon` (77.583) / `place` (Jayanagar, Bengaluru), `timezone` (Asia/Kolkata), `fail_hours` (6), `url` |
| `kshan_events` | `logs/kshan/events.md` (+ `logs/rahi/events.md`), `event.md`, `event.md.bck` | `event.md` (new drafts only) | `max_age_days` (30) |
| `watch_rewards` | rewards.bing.com/redeem/in (+ /shop, /donate, /win) | `logs/watch/rewards.yml`, outbox on change | `market`, `pages`, `notify_price` (true), `notify_removed` (false), `notify` ([telegram]) |
| `watch_gold` | cleartax.in/gold-rate-{city} | `logs/watch/gold.yml`, outbox + phone inbox on change | `city` (bangalore), `karat` (22), `url`, `pattern`, `notify` |
| `watch_domains` | domainoffer.net, tld-list.com | `logs/watch/domains.yml`, outbox + phone inbox on change | `tlds` ([ar]), `rank_by` (total), `top` (1), `sources`, `max_pages` (5), `notify` |

### 6.3 Records engine

All vault data files are handled as `yaml.Node` trees, not structs:

- Unknown fields survive in place; new fields append at the end (Python dict semantics).
- Scalar styles/quoting survive; collections are block style.
- Dates Kronos writes are single-quoted `'2026-10-06'`.
- Python value semantics: `Equals` matches string values only; `Truthy` = Python truthiness; `DateOf` accepts unquoted timestamps or strict ISO (`YYYY-MM-DD`/`YYYYMMDD`); text uses `PyStr` (null → `None`, `true` → `True`, `10.50` → `10.5`, lists → `['a', 'b']`).
- Load errors (Taskmaster-identical): `Missing file: <path>` · `Invalid YAML in <name>: …` · `<name>: root must be a YAML mapping` · `<name>: missing '<key>' list` · `<name>: '<key>' must be a list of mappings`. A falsy list (`~`, `[]`, `''`) is empty.
- Save: `updated: <YYYY-MM-DD HH:MM:SS>\n\n` + `{key: records}`, 2-space indent; refuses an empty list. Unreadable files are never treated as empty, so a YAML typo can never cause a wipe.

### 6.4 `event.md` (shared with Omnimo + Natlas)

```yaml
updated: <ts>
events:
  - id: <unique>              # auto: sub-<slug(name)>-<YYYY-MM-DD>
    title: <str>
    date: '<YYYY-MM-DD>'
    status: open | pending | closed
    frequency: none | daily | weekly | monthly | quarterly | yearly   # also "N days|weeks|months|years"
    priority: high | medium | low
    last_done: '<YYYY-MM-DD>'  # set on recurring reopen
    anchor_day: 29..31         # Kronos-only, see below
```

`event_update`, in order: normalise legacy (`once` → `none`, `normal` → `medium`) → validate (warn only) → one-time active past → today + pending → recurring active past → roll forward to ≥ today → recurring closed → advance ≥ 1 step then roll to ≥ today, `last_done` = old date, open → purge closed one-time older than 365 d → stable sort (active first, date, priority; unparseable date last; unknown priority 99).

`event_archive`: closed + one-time + date < today − 30 → `event.md.bck` (dedupe by id), skip if nothing, refuse if it would empty `event.md`.

**Anchor day.** Month/year rules step with an anchor: `AddMonths(d, n, anchor)` = target month, day `min(anchor, days_in_month)`. Anchor = stored `anchor_day` if the date still matches it, else the current day. After a move: store `anchor_day` if ≥ 29, delete otherwise. 31 Jan → 28 Feb → 31 Mar; 29 Feb → 28 Feb → 29 Feb (leap). Applies to events and subscriptions; Natlas and Omnimo must preserve the field.

### 6.5 `subscription.md` and Arthik

```yaml
subscriptions:
  - name: <str>
    status: active | cancelled
    next_due_date: '<YYYY-MM-DD>'
    cycle: <rule>
    anchor_day: 29..31        # optional, automatic
    # other keys preserved (amount, account, category …)
```
`subscription_update`: active + overdue → roll forward (unknown cycle → WARN); then due == today → pending event `sub-<slug>-<date>` in `event.md` (idempotent). `event.md` unusable → WARN, subscriptions still saved. Legacy `active:`/`cancelled:` lists are read and written back as `subscriptions:`. Sort: active first, then due date.

`arthik_subscriptions` writes `data/finance/inbox/kronos.md` (only Kronos writes it; Arthik only reads it):

```yaml
updated: <ts>

last_run: '<YYYY-MM-DD>'
drafts:
  - id: sub-<slug(name)>-<YYYY-MM-DD>   # = the due event's id
    source: subscription
    date: '<YYYY-MM-DD>'
    title: <subscription name>
    type: expense
    amount: <from subscription.md>
    account: <from subscription.md>
    category: <from subscription.md>
    notes: Subscription · <cycle>
```
Drafted when its `sub-…` event is dated after `last_run` and up to today, or its `next_due_date` is today. First run: today only; catch-up ≤ `max_catch_up_days`. Drafts older than `keep_days` dropped. Empty list → `drafts: []`.

### 6.6 `calendar.md`, agenda and briefing

```yaml
type: calendar
version: 1
last_sync: '<YYYY-MM-DD>'
events:                        # sorted by date, time ('' first), title
  - id: <Google event id>      # per repeat (singleEvents=true)
    title: <summary | (no title)>
    date: '<YYYY-MM-DD>'
    end: '<YYYY-MM-DD>'        # multi-day only; last day, inclusive
    time: '<HH:MM>'            # timed only; container TZ
    category: <tag>            # birthday | anniversary | holiday | festival | reservation | out_of_office | focus_time | working_location | event | options.categories
    calendar: <calendar name>
    location: <str>            # when set
```
Calendars: `calendarList` (hidden skipped) minus `exclude` (name or id). Category: `eventType` birthday (+ anniversary), holiday calendar (`#holiday@`; description starting `Observance` → festival), `fromGmail` → reservation, outOfOffice/focusTime/workingLocation, else event; `options.categories` (calendar → tag) overrides. Window: today − `past_days` … + `ahead_days`. Cancelled dropped. Rewritten in full. Any calendar failing or 0 events → FAIL, file kept. Token refreshed when missing/expired (4 min margin) via `token_uri` + refresh token; written back atomically 0600 (not in dry runs); `invalid_grant` → hint to regenerate.

`inbox/daily.md`: `# Overdue` / `# Due Today` / `# Upcoming` (7 d), sorted, blank line between. Prefixes `EVENT:` · `SUBSCRIPTION-` · calendar entries by category in capitals (`BIRTHDAY-`, `HOLIDAY-`, `FESTIVAL-` …; `event` → `CALENDAR-`; `'s birthday` dropped; timed entries get ` HH:MM`). Pending events are always overdue. A calendar entry is *today* from `date` to `end`. Broken source → agenda written anyway, then FAIL (`Agenda incomplete – unreadable: <files>`).

`nimo_briefing`: `daily.md` as an outbox `briefing` (Omnimo adds Nimo's commentary); appends `# Pulse` + `state/brief.yml` lines when that file is dated today.

### 6.7 `health.md` → health site

`meta` (title, tagline, subtitle, footer, last_updated, weight, height, bmi?, body_fat) · `runs.planned[]` / `runs.recent[]` (name, date, link?, any keys → label/value, `reward` highlighted) · `gym[]` (day, title, exercises[], full_width?) · `daily[]` (date, steps, sleep_h, deep_h, rem_h, light_h, awake_h, hr_rest, hr_avg, hr_max, kcal_total, kcal_active).

One `index.html` in the nss theme: classless, no JavaScript, inline SVG favicon, stylesheet from `assets/health.css`. Extras: stats strip (races, total km, best 10K, money spent, weight/height/BMI/body fat), pace when `distance` parses, countdown on planned runs, runs sorted by date. Dates: unquoted YAML dates, `25 Oct 2026`, `25 October 2026`, `2026-10-25`, `25-10-2026`. Quote times (`'52:00'`). `health_daily.go`: four inline-SVG cards from the latest 30 `daily` entries — steps (bars + average), sleep stages, heart rate (gaps for missing), calories; empty cards skipped; bad entries WARN.

### 6.8 Pulse (`logs/pulse/` + the phone's folders, package `internal/pulse`)

The phone is the only writer of `<daily_logs>/YYYYMMDD.md` — Kshan (`logs/kshan/daily`), RahiOS before it (`logs/rahi/daily`, still read for days Kshan doesn't have). YAML, RahiOS schema; `final: true` once closed (after a health read, a lasting health error, or 12:00 next day). Kronos never writes there. Null = *missing*; `health_error` (Kshan: `no_data`, `permission`, `unavailable`) → steps/sleep missing; a tracked app absent from `apps:` on a day with screen data = 0 min. Kshan has no `mindful`, `modes` or `created`: absent = missing (no turn-backs), never zero.

Folders (`Dirs`, from paths): **pulse** (`logs/pulse/`: `goals.md` — once, commented defaults, yours to edit — `baseline.md`, `state/{brief.yml, sent.yml, intraday-YYYYMMDD.yml}`, `weekly/YYYY-Www.md`, `monthly/YYYYMM.md`), **daily_logs** (+ `daily_logs_old`), **phone_inbox** (`<id>.md` written here; `read/` + `phone_inbox_old/read/` read for feedback). Up to 1.2.1 Pulse's own files lived in `logs/rahi/`; move them once to `logs/pulse/`.

- **Baseline:** `State{days_with_data, metrics{key: {weekday/weekend {median_7d, median_28d}, goal {start, current, missed_weeks, history}}}, rolled_days/weeks/months}`. Medians over the last 7/28 days of the same day type (all days when < 3). Recomputed each run; written only on change. Observe-only until 7 days.
- **Goals:** screen, `app:<pkg>` (lower better), steps, sleep (higher better). Start at the 28-day median; weekly (Sunday final): week median meets → ×0.95 / ×1.05; 2 missed weeks → loosen 5%, never past start; a goal started that week isn't judged; `Clamp` to `goals.md` limits.
- **Target:** yesterday worse than baseline → yesterday ×0.9 (×1.1 higher-better), never stricter than the goal.
- **Strikes:** over = > 125% (or < 75% for steps/sleep) of baseline; pattern = 2 of 3 or 3 of 7 days. Warnings for days ≤ 2 days old, one per metric per run and per 3 days.
- **pulse_watch:** snapshot each run; app nudge = today > 7-day median ∧ pattern ∧ not backed off; steps nudge after `step_nudges.soft`/`.final` when steps + `StepsStillToCome` < target (learned from ≥ 14 goal days of snapshots, else typical total × remaining waking share).
- **Messages:** `Plan` orders pattern > steps-final > app > steps-soft; cap = `max_messages_per_day` minus today's sent (reports and deferred excluded); quiet hours or no budget → nudges dropped, patterns deferred to the next briefing (`channel: briefing`). IDs are deterministic (`nudge-<app>-YYYYMMDD`, `steps-final-YYYYMMDD`, `pattern-…`, `week-…`). Inbox file = frontmatter `{id, title, from: Kronos, date, type}` + body. Back-off: last 3 read nudges of a type dismissed or ended off target (a mindful turn-back counts as improvement) → every other day for 7 days. RahiOS 0.0.5 `read_at`/`dismissed` used, file-name day as fallback.
- **Reports:** latest weekly/monthly not yet sent, outside quiet hours → `notice` with plain numbers (`ReportText`) + short weekly inbox file.
- **Brief:** `state/brief.yml {date, lines}` rebuilt every run: learning n/7, or yesterday (provisional until final) ✓/✗ per goal, "well off your usual", today's targets, deferred warnings.
- **Testing:** `kronos pulse-fixture --out DIR` writes fake logs (patterns: `--heavy`, `--streak`, `--null-health`, `--health-error`, `--missing-app`, partial today `--today-at/--today-steps`), snapshots and `goals.md` into a folder marked `.pulse-fixture` (`--force` wipes only such a folder). `kronos run pulse_* --sandbox DIR --now … --dry-run`: messages printed as `~ would send [p<priority>|report] <channel> <id> — <title>`; Pulse's own files still written in the sandbox.
- **Planned (Omnimo):** a `report` outbox kind — `{kind: report, title, text (plain-numbers fallback), prompt: pulse_weekly|pulse_monthly, data: <rollup YAML>}`; Omnimo makes one AI call with `nimo.yml reports.<prompt>` as system and `data` as user, sends title + prose, or `text` if the AI fails. Kronos side then: `outbox.Report` + switch `sendReports` from `Notice`.

### 6.9 Monitors (`logs/watch/`, package `internal/watch`)

Own package, folder and `paths.watch`; shares only the scheduler, the outbox and the phone inbox. A monitor is `Watcher[T]{Name, Title, Notify, Fetch, Show, Diff, Keep}` registered as `watch_<Name>`; `Run`:

1. `Fetch` → current `T` (+ warnings = a source failed). Error → `failures.yml[name]++`; WARN, and exactly at `fail_after` (3) one FAIL + alert; the next success clears it.
2. **Dry run**: print `Show(cur)` and what would happen; no file, no message, no failure count.
3. No state → store `<watch>/<name>.yml` (`updated`, `data`), send "now watching" + `Show`.
4. Warnings with stored values: `Keep(old, cur)` if defined (rewards: union by SKU), else skip (a partial fetch is never a change).
5. `data` equal (YAML form) → SKIP, file untouched. Else store; `Diff` non-empty → send; empty → stored silently.
6. Channels (`options.notify`): `telegram` → outbox `notice` (title + body, ≤ 3800 chars); `kshan` (old name `rahi` still accepted) → `<phone_inbox>/watch-<name>-YYYYMMDDHHMM.md` (first run `-start`). Defaults: rewards `[telegram]`, gold/domains `[telegram, kshan]`.

HTTP: browser User-Agent, `timeout_s` (30), one retry after 5 s on network error/5xx, 8 MB cap. HTML via regex helpers; tests pin each page shape, `--dry-run` shows layout changes at once.

- **rewards** (`Catalog{items: [{sku, title, points, category, desc}]}`): cards = `<a href=…/redeem/sku/<id>>`; title = img alt, else heading, else text; points = `N pts|points`. 0 pages or 0 items → error. Notifies new items and points changes (removals optional); text-only changes silent.
- **gold** (`Gold{city, karat, per_gram}`): `pattern` (with `{karat}`) on the page text; group 1 → number. Diff `▲/▼ ₹d from ₹old (p%)`.
- **domains** (`Domains{rank_by, tlds: {ar: [{registrar, score, transfer, renew, register, link}]}}`): domainoffer `#domain-list` (ICANN-fee column skipped, `Next` → more pages) + tld-list table by header; registrar aliases merged; best quote per registrar; cheapest `top`. A failing source is a WARN; no prices → error.

### 6.10 Kshan (`logs/kshan/`, package `internal/kshan`)

**`kshan_weather`** — `@`start (`run_at_start`), hourly (:20) and on change of `logs/kshan/weather/location.md` (`watch:`; the scheduler waits until the file is unchanged for a tick, so Syncthing's temp-file writes settle first).

1. **Location:** `location_file` (Kshan is its only writer; Kronos never writes it). YAML `{updated, lat, lon, accuracy_m, place, reason}`, plain or inside `---` frontmatter. Missing → the fallback options `lat`/`lon`/`place` silently; unreadable, or no valid lat/lon → the fallback with a WARN; no `place` → the fallback place.
2. **Skip rule:** no network call when `weather_file` exists, is younger than `every` − 10 min (default 50 min) and `location.md` is not newer than it — unless the location's `reason` is `tap`. A dry run always fetches.
3. **Fetch:** GET `<url>?latitude=<lat>&longitude=<lon>&current=temperature_2m,apparent_temperature,weather_code,is_day,relative_humidity_2m&hourly=temperature_2m,weather_code,precipitation_probability&daily=temperature_2m_min,temperature_2m_max,precipitation_probability_max&timezone=<timezone>&forecast_days=2` (30 s timeout).
4. **Write** `weather_file` atomically, YAML built as nodes:

```yaml
# Kshan weather - written by Kronos (open-meteo). YAML.
updated: '2026-11-12 21:20:00'
place: Jayanagar, Bengaluru
lat: 12.925
lon: 77.583
now: {temp_c: 22.5, feels_c: 23.0, code: 2, text: Partly cloudy, is_day: false, humidity: 81.0}
today: {min_c: 18.2, max_c: 29.0, rain_pct: 65.0}
next: # the next 6 whole hours after now, nearest first
  - {time: '22:00', temp_c: 21.9, code: 3, rain_pct: 20.0}
```
Numbers to 1 decimal (lat/lon as given, up to 6 decimals), codes as integers, times/dates single-quoted, `null` for a value open-meteo didn't give. `next` = the first `hours` hourly slots after `current.time`. Words: 0 Clear, 1 Mostly clear, 2 Partly cloudy, 3 Cloudy, 45/48 Fog, 51–57 Drizzle, 61–67 Rain, 80–82 Showers, 71–77/85/86 Snow, 95–99 Storm, else `—`.
5. **Log:** one line per run — `<place> (<lat>, <lon>): <temp> °C, <words> – <why>` or `SKIP … weather.md is N min old, location unchanged – <why>`; why = startup / hourly / location changed / manual (from `Ctx.Reason`).
6. **Failures:** keep the old file (WARN) and record `{since, alerted}` in `kshan_weather.fail.yml` next to `state.json`; once failing ≥ `fail_hours` (6) → one FAIL + alert; the next success deletes the mark ("Weather working again"). Dry run: prints the diff, writes nothing, no mark.

**`kshan_events`** — every 30 min (:15/:45). Reads `paths.kshan_events` (Kshan: `drafts: [{text, created}]`, text like `dentist fri 5pm`) and `paths.kshan_events_old` (RahiOS: `drafts: [{id, title, date, time, created}]`); missing file = nothing; unparseable → FAIL, nothing written. Kshan draft → `ParseWhen(text, created day)`: date words `today`, `tomorrow`/`tmrw`, weekdays (`fri` = on/after; `next fri` = after), `14 nov`, `nov 14 2027`, `14th november`, `15/12`, `2026-12-20`; times `5pm`, `5 pm`, `9:30am`, `17:00`, `noon`; linking `on`/`at`/`by` before them dropped; no date → the draft's day; a day/month already past → next year; the rest is the title (first letter capitalised). Id = `kshan-<created YYYYMMDD-HHMMSS>` (no stamp → `kshan-<sha1(text)[:10]>`); RahiOS drafts keep theirs. New ids (not in `event.md` or `event.md.bck`, draft not older than `max_age_days` 30) are appended as `{id, title (+ " HH:MM"), date, status: open, frequency: none, priority: medium}`; `event.md` is saved only when something was added. Kronos never writes the drafts files (Kshan can hide a draft whose `kshan-<created>` id is in `event.md`).

---

## 7. Configuration rules

- `kronos.yml` (gitignored) = `kronos.yml.example` + your options. Container paths only; host folders come from `.env` mounts.
- Per job: `schedule`, `description`, `enabled`, `timeout`, `catch_up`, `skip_if_busy`, `run_at_start`, `watch`, `command`, `options`.
- `settings`: `tick_seconds` (10), `timeout_seconds` (900), `catch_up_hours` (48), `alerts` (true).
- Every job in a module must have an entry in `kronos.yml.example`; `registry_test.go` fails otherwise.

---

## 8. Outbox, and no locks

**Outbox** — producer side of Omnimo's contract: `<dir>/<unix-ns>-<kind>.json`, written as `.json.tmp` then renamed. JSON v1: `{version: 1, kind: notice|alert|briefing, source: "kronos", title?, text (trimmed, non-empty), created: RFC3339}`. Omnimo sends `text` (a notice's title is not shown, so monitors put it in the text). Dir must be writable by `PUID`.

**No file locks.** Any app may edit the data files at any time (Obsidian, Natlas, Omnimo, Kronos). Kronos reads, changes and atomically replaces a file within milliseconds; an edit saved in that same instant could be lost — accepted trade-off.

---

## 9. Adding things

- **A vault job:** `internal/jobs/<name>.go` with `func init() { job.Register("<name>", fn) }` + `<name>_test.go` + a `kronos.yml.example` entry; rebuild. Template: `archive_rollover.go`. Use `c.Path`, `c.Int/String/Bool/Strings`, the dry-run aware `c.WriteFile/AppendFile/Move/Remove/MkdirAll`, `c.Notify`, `store.*`, `dates.*`, and `c` as the context for network calls. Return an error to FAIL + alert; `c.Log.Skip` + nil for "nothing to do".
- **A monitor:** copy `internal/watch/gold.go` (+ test): `Fetch`, `Show`, `Diff`; register; add `watch_<name>` to the example.
- **A module:** new package under `internal/` with a `doc.go`, jobs registered in `init()`, import it in `main.go` and `registry_test.go`, jobs in the example, a section here.
- **A script job:** file in `./scripts` + `command:` entry + restart (no rebuild).
- **A new path:** `config.defaultPaths` + the example's `paths:` (+ a mount and `.env` variable if it is outside the vault).

Remove a job: delete its files and its example/kronos.yml entries; a job-only path can go from `defaultPaths`.

---

## 10. Tests

`go test ./...` (also in `docker build` and CI):

- **core:** dates (Python-equal rules, anchor), store (error messages, round-trip order, PyStr/PyFloat/escape, UniquePath), outbox (contract), config, scheduler (ok/fail/panic/timeout, command env/exit/stderr/timeout, bad jobs alerted, catch-up order, Prev, watch debounce, skip_if_busy, run_at_start), CLI test flags.
- **jobs:** anchor drift, legacy normalisation, broken file never overwritten, dry run writes nothing, subscription event idempotency, calendar sync (fake Google: categories, times, multi-day, cancelled, sort, file kept on failure), agenda from calendar.md, partial agenda, arthik drafts window/dedupe/prune, health daily cards, briefing + Pulse block, registry = kronos.yml.example.
- **pulse:** parse/nulls, medians, over/pattern, goals tighten/loosen/clamp, target, step forecast, plan/cap/quiet/defer, read feedback + back-off, rollup/report; jobs: goals start, warning once + defer, weekly report, observe-only, app/step nudges, bad log tolerated, `daily/` untouched, dry run prints + writes only in a sandbox; fixture patterns/determinism/refuses a non-fixture folder.
- **pulse (1.2.2):** Kshan log without mindful/modes/created, folder fallbacks (day, dates, read feedback, off).
- **kshan:** weather golden file, location.md parsing (missing/garbled/frontmatter → fallback), skip rule (fresh, newer location, tap, stale, dry run), run reason, options, failures (warn → one alert at 6 h → recovery), dry run, WMO words; events import (Kshan + RahiOS, archived/old skipped, idempotent, drafts untouched), errors, fallback off; date phrases.
- **watch:** baseline + one message, same value → file untouched and no message, change → file + Telegram + phone inbox, notify option, dry run stores nothing, fail_after alert once + reset, partial fetch not stored; rewards cards/points/silent text change/options/merge; domains parsing, aliases, ranking, diff; gold pattern 22K/24K, missing rate, diff, end-to-end dry run.

---

## 11. Differences from Taskmaster (intended)

| Area | Taskmaster | Kronos |
|---|---|---|
| Telegram | own bot token | outbox → Omnimo (no token in Kronos) |
| Briefing AI | called old nimo:1418 | Omnimo writes the commentary |
| Month-end dates | drift (31 Jan → 28 Feb → 28 Mar) | `anchor_day` keeps 31 Mar |
| `once` / `normal` | `once` never became pending | read as `none` / `medium`, normalised |
| Missed runs | skipped | caught up once on start (48 h) |
| Health site | nightly only, 6-theme PWA | nightly + on `health.md` edit; single nss page, no JS |
| Alerts | "Exited with code 1" | the real error message |
| Schedule | 01:10–03:10 | 02:05–03:45 + 05:40 briefing (§6.1) |
| YAML | PyYAML re-quoted, dropped comments | quoting/comments kept; content identical |
| New | — | `@manual`, `watch:`, `command:`, `options:`, `skip_if_busy`, `run_at_start`, `--dry-run`, `list/next/health/notify`, Pulse, monitors |

Parity check (2026-10-05): an edge-case fixture set run through every Python task and Go job — all 16 outputs matched (YAML content and order, `daily.md`, `note.md`, health-site files byte for byte, inbox/archive layout, outbox messages).

---

## 12. Known limits

- Jobs are sequential; a slow job delays the next (all jobs < 1 s except network ones, bounded by their timeouts).
- An abandoned (timed-out) Go job keeps running in the background until it returns.
- `event_archive` ages events by `date`, not close date.
- Daemon crash count never resets.
- Catch-up only for jobs that ran at least once under Kronos.
- File watch is polling, not inotify.
- Monitors scrape public pages: a site redesign shows up as a failed check (alert after `fail_after`); fix the pattern/parser.

---

## 13. Versions

| Version | Change |
|---|---|
| 1.0 | Taskmaster port; outbox; calendar_sync; health site nss theme |
| 1.1.0 | Pulse (pulse_watch, pulse_rollup, briefing block) |
| 1.1.1 | Pulse test tooling: pulse-fixture, `--now`, `--rahi-path` |
| 1.1.2 | Monitors (watch_rewards, watch_gold, watch_domains), `skip_if_busy` |
| 1.2.1 | Modules (Pulse jobs moved into `internal/pulse`), health site via `.env KRONOS_HEALTH_SITE`, schedule plan, docs folded in here |
| 1.2.2 | Kshan replaces RahiOS: module `internal/kshan` (kshan_weather from Kshan's location.md with a skip rule, kshan_events), `Ctx.Reason`, phone paths as config (`daily_logs`, `phone_inbox`, `kshan_events` + `_old` fallbacks), Pulse files → `paths.pulse`, notify channel `kshan`, `run_at_start`, `--sandbox` |

Ask for changes by sharing this file plus only the file(s) being changed.
