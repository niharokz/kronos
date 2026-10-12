# Kronos — Architecture (internal)

> Internal reference for the Nimory homelab. Share this file (plus only the files being changed) instead of the full source.
> Contains no secret values — only names and paths. Last reviewed: 2026-10-10 (v1.0.0, replaces Taskmaster).

---

## 1. Context

Kronos is the time-driven half of Nimory: nightly maintenance of the vault's data files, the agenda, the health site, and the morning briefing. It replaced **Taskmaster** (Python) with a 1:1 port in Go — same job names, rules, order and log messages.

| Project | Role | Shares with Kronos |
|---|---|---|
| **Omnimo** (Go) | Telegram bot + AI (Nimo) | `event.md`, the **outbox** folder |
| **Natlas v2** (Go) | Web UI for the data files | `event.md`, `subscription.md`, `health.md`, `birthdays.md` |
| **Kronos** (Go) | Scheduler | — |

Kronos never talks to Telegram or any AI. It writes outbox files; Omnimo sends them.

Host: a small Debian box with Docker Compose. Repo: `gitlab.com/niharokz/kronos` (mirrored to GitHub/Codeberg), deployed from a fresh clone. All host-specific values (folders, UID, time zone) live in the gitignored `.env`.

---

## 2. Deployment

| Item | Value |
|---|---|
| Image | multi-stage: `golang:1.23-alpine` build (runs `go vet` + `go test`) → `alpine:3.20` + `ca-certificates`; static binary `/app/kronos` (~7 MB), on `PATH` |
| Command | `kronos serve` |
| User / hardening | `.env` `PUID:PGID` (image default `USER 1000:1000`), `init: true`, `read_only: true` + tmpfs `/tmp` (16 MB), `cap_drop: [ALL]`, `no-new-privileges`, `pids_limit: 256`, `mem_limit: 256m`, `stop_grace_period: 1m` |
| TZ | `.env` `TZ` (default UTC); zone data is compiled in (`time/tzdata`), so it works on minimal images |
| Volumes | `$NOTE_PATH → /data/notes` · `$WEB_PATH → /docker/web` · `./kronos.yml → /app/kronos.yml:ro` · `./config → /app/config` (rw: token refresh) · `./scripts → /app/scripts:ro` |
| Network | default bridge (only `people.googleapis.com` / `oauth2.googleapis.com` outbound); no ports |
| Secrets / host values | `.env` (gitignored, template `.env.example`): `DATA_PATH` (base), `NOTE_PATH` (`${DATA_PATH}/notes`), `WEB_PATH`, `PUID`, `PGID`, `TZ` — used only by compose, not passed into the container. Google OAuth in `./config/token.json` (+ `credentials.json`, only for regenerating) |
| Ignored | `.gitignore`: `.env*` (except `.env.example`), `kronos.yml`, `config/*`, `scripts/*` (except `*.example`), `**/token.json`, `**/credentials.json`, `**/client_secret*.json`, build output, editor/OS files · `.dockerignore`: the same secrets + `.git`, CI file, docs |
| Logs | stdout (json-file 10 MB × 3) + `/data/notes/logs/kronos/kronos_YYYYMM.log` |
| State | `/data/notes/logs/kronos/state.json` |

Rebuild after code changes (`docker compose up -d --build`); restart after `kronos.yml` changes; nothing needed after adding a script.

CI (`.gitlab-ci.yml`): `gofmt -l` must be empty, `go mod verify`, `go vet`, `go test`, build. Config parsing rejects unknown keys at the top level, under `settings:` and inside a job (`paths:` and `options:` stay free-form).

---

## 3. Code map

```
cmd/kronos/main.go       CLI: serve | run <job> [--dry-run] | list | next [n] | health | jobs | notify | version
internal/config/         kronos.yml → Config{Paths, Settings, Jobs(ordered)}; default paths; Job.Int/String/Bool/Strings(options)
internal/scheduler/      scheduler.go: kinds, queue, catch-up, watches, daemons · run.go: Execute (timeout, panic), command jobs, Alert
internal/job/            Register/Lookup/Names, Ctx (paths, options, file ops, Notify, DryRun), Logger, Diff
internal/jobs/           one file per job + helpers.go + assets/health.css (nss stylesheet, verbatim from health_website.py)
internal/store/          YAML as yaml.Node records: LoadMapping/LoadRecords/RecordList/RecordsYAML/Encode, Get/Set/Delete,
                         DateOf/IntOf/Field/Equals/Truthy; PyStr/PyRepr/PyFloat/PyHTMLEscape; AtomicWrite(Perm), UniquePath
internal/dates/          ParseISO/ParseTimestamp, AddMonths/AddYears (anchor), ParseRule/Advance/RollForward, AnchorDay
internal/outbox/         Write(dir, source, kind, title, text) — producer side of Omnimo's contract
internal/state/          Load, Record(path, job, started, duration, err) — atomic JSON
```

Dependencies: `gopkg.in/yaml.v3` (via its GitHub mirror — `replace` in go.mod, same as Omnimo), `github.com/robfig/cron/v3`. Google is plain HTTPS (no SDK).

---

## 4. Scheduler

- **Kinds**: 5-field cron (robfig `ParseStandard`, local TZ) · `@startup` · `@daemon` · `@manual`. Invalid schedule or unknown job (no Go registration and no `command:`) → `FAIL` + alert at load, job skipped, others run.
- **Queue**: one worker, strictly sequential. `busy` flag = never queued twice. Cron `next` is cleared on enqueue and recomputed from *finish* time (Taskmaster semantics: overruns skip slots).
- **Startup order**: `@startup` jobs (config order) → catch-ups (slot order) → normal schedule.
- **Catch-up**: for cron jobs with `catch_up` (default true) that have a state entry, if the latest slot within `catch_up_hours` (48) is after `last_run`, run once with reason `catch-up for <slot>`. No state entry (never ran under Kronos) → no catch-up. A failed run counts as a run (no retry loops).
- **Watch**: every tick, stat each watched file (size + mtime). Change → pending; unchanged for ≥ one tick → enqueue (`changed: <file>`). After any run of that job, its signatures are refreshed (its own writes never re-trigger it).
- **Timeouts**: per job `timeout` or `settings.timeout_seconds` (900). Go job: `Execute` returns `Timed out … abandoned`, goroutine may finish in background. Command: SIGTERM, then kill after 10 s (`WaitDelay`).
- **Panics** in Go jobs → `crashed: <value>` + stack to stdout.
- **Daemons**: own goroutine/process; exit → crash count, retry after 30 s, give up after 5 (alert). On shutdown: ctx cancel / SIGTERM, wait 30 s.
- **Shutdown**: SIGTERM → stop queueing → wait for the running job → stop daemons → exit 0.
- **Alerts**: every `FAIL` → `outbox.Write(alert, title=<job>, text=<error>)`; failure to write the alert → `WARN Alert not sent`. `settings.alerts: false` disables. `kronos run` (manual) never alerts.
- **State**: after every scheduled or manual (non-dry) run: `last_run, last_status (ok|fail), last_ok, last_error, duration_ms`.

### Command jobs

`exec` with container env + `KRONOS_<PATHKEY>` for every `paths:` entry (upper-cased) + `KRONOS_JOB`, `KRONOS_CONFIG`, `KRONOS_DRY_RUN=0|1`. stdout/stderr → container log. Non-zero exit → `Exited with code N` + last 5 stderr lines.

---

## 5. Jobs and schedule (IST)

| Time | Job | Reads | Writes | Options (default) |
|---|---|---|---|---|
| start | `restarted` | `/proc/meminfo`, `/proc/uptime` | outbox notice | `label` (Nimory) |
| 02:00 | `untitled_import` | `inbox/*untitled*` | `inbox/note.md` (append), deletes sources | `note_file` (note.md) |
| 02:05 | `inbox_archive` | `inbox/*` by mtime | `archive/` via `UniquePath` | `archive_after_days` (30), `exclude` ([note.md, guide.md]) |
| 02:10 | `subscription_update` | `subscription.md`, `event.md` | both | — |
| 02:12 | `arthik_subscriptions` | `subscription.md`, `event.md`, `data/finance/inbox/kronos.md` | `data/finance/inbox/kronos.md` (only Kronos writes it) | `max_catch_up_days` (3), `keep_days` (60) |
| 02:15 | `event_archive` | `event.md`, `event.md.bck` | backup first, then `event.md` | `archive_after_days` (30) |
| 02:20 Mon | `birthday_sync` | People API, `config/token.json` | `birthdays.md` (+ token on refresh, 0600) | — |
| 02:30 | `event_update` | `event.md` | `event.md` | `purge_after_days` (365) |
| 02:45 + watch | `health_website` | `health.md` | `<paths.health_site>/index.html` | — |
| 03:00 | `daily_update` | events, subscriptions, birthdays | `inbox/daily.md` | `upcoming_days` (7) |
| 03:30 1 Jan | `archive_rollover` | `archive/*` | `archive/<year-1>/` | — |
| 05:40 | `nimo_briefing` | `inbox/daily.md` | outbox briefing | — |

Paths relative to `/data/notes/`. Real paths come from `paths:` in kronos.yml (defaults in `internal/config`; the code default for `health_site` is generic, the server's kronos.yml sets the real domain).

---

## 6. Data contracts

### Records engine

All data files are handled as `yaml.Node` trees, not structs:

- Unknown fields survive in their original position; new fields append at the end (Python dict semantics).
- Original scalar styles/quoting survive; collections are forced to block style (PyYAML `default_flow_style=False`).
- Dates Kronos writes are single-quoted strings `'2026-10-06'` (same as PyYAML).
- Value semantics follow Python: `Equals` matches only string values; `Truthy` = Python truthiness; `DateOf` accepts unquoted timestamps (date part) or strict ISO strings (`YYYY-MM-DD`/`YYYYMMDD`); text built from values uses `PyStr` (null → `None`, `true` → `True`, `10.50` → `10.5`, lists → `['a', 'b']`).
- Load errors (messages identical to Taskmaster): `Missing file: <path>` · `Invalid YAML in <name>: …` · `<name>: root must be a YAML mapping` · `<name>: missing '<key>' list` · `<name>: '<key>' must be a list of mappings`. A falsy list value (`~`, `[]`, `''`) is an empty list.
- Save: `updated: <YYYY-MM-DD HH:MM:SS>\n\n` + `{key: records}`, 2-space indent; refuses an empty list (`Refusing to save an empty '<key>' list to <name>`). Unreadable files are never treated as empty, so a YAML typo can never cause a wipe.

### `event.md` (shared with Omnimo + Natlas — schema per Omnimo ARCHITECTURE "Shared events file")

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

`event_update` steps, in order: normalise legacy (`once` → `none`, `normal` → `medium`) → validate (warn only) → one-time active past → today + pending → recurring active past → roll forward to ≥ today → recurring closed → advance ≥1 step then roll to ≥ today, `last_done` = old date, open → purge closed one-time older than 365 d → stable sort (active first, date, priority; unparseable date last; unknown priority 99).

`event_archive`: closed + one-time + date < today − 30 → `event.md.bck` (dedupe by id; type-aware), skip if nothing to archive, refuse if it would empty `event.md`.

### Anchor day (`anchor_day`)

Month/year rules (`monthly`, `quarterly`, `yearly`, `N months`, `N years`) step with an anchor: `AddMonths(d, n, anchor)` = target month, day `min(anchor, days_in_month)`. Anchor = stored `anchor_day` if the current date still matches it (`day == min(anchor, days_in_month)`), else the current day. After a date move: store `anchor_day` if ≥ 29, delete it otherwise. Effect: 31 Jan → 28 Feb → 31 Mar; 29 Feb → 28 Feb (non-leap) → 29 Feb (leap). Manually moving the date to another day resets it. Applies to events and subscriptions. Natlas v2 must preserve the field (Omnimo does, via `Event.Extra`).

### `subscription.md`

```yaml
subscriptions:
  - name: <str>
    status: active | cancelled
    next_due_date: '<YYYY-MM-DD>'
    cycle: <rule>
    anchor_day: 29..31        # optional, automatic
    # other keys preserved
```
Active + overdue → roll forward (unknown cycle → WARN, skipped); **then** due == today → pending event `sub-<slug>-<date>` in `event.md` (idempotent). `event.md` unusable → WARN, subscriptions still saved. Legacy `active:`/`cancelled:` read, written back as `subscriptions:`. Sort: active first, then due date.

### `birthdays.md`
```yaml
type: birthdays
version: 1
last_sync: '<YYYY-MM-DD>'
people:
  - id: people/<id>
    name: <str>
    birthday: '<YYYY-MM-DD>'   # 0000 when year unknown; sorted by MM-DD
```
0 contacts returned → FAIL, file kept. Token: refreshed when missing/expired (4 min margin) via `token_uri` + `refresh_token/client_id/client_secret`; written back atomically 0600 (not in dry runs).

### `inbox/daily.md`
`# Overdue` / `# Due Today` / `# Upcoming` (7 d), each sorted, sections separated by a blank line. Prefixes `EVENT:` · `SUBSCRIPTION-` · `BIRTHDAY-`. Pending events are always overdue. Due-today subscriptions appear only via their auto event. Birthdays wrap New Year; impossible dates → 28 Feb. Broken source → agenda written anyway, then FAIL (`Agenda incomplete – unreadable: <files>`).

### `health.md` → health site
`meta` (title, tagline, subtitle, footer, last_updated, weight, height, bmi?, body_fat) · `runs.planned[]` / `runs.recent[]` (name, date, link?, any keys → label/value, `reward` highlighted) · `gym[]` (day, title, exercises[], full_width?).

Since 2026-10-10 the page uses the **nss theme** (port of the nss-edition `health_website.py`): one `index.html`, classless and JavaScript-free, inline SVG favicon, stylesheet from `assets/health.css`. Computed extras: stats strip (races, total km, best 10K, money spent, weight/height/BMI/body fat), pace after `time` when `distance` parses, countdown on planned runs, runs sorted by date (planned soonest first, recent newest first, unparseable dates last). Dates accepted: unquoted YAML dates, `25 Oct 2026`, `25 October 2026`, `2026-10-25`, `25-10-2026`. Times should be quoted (`'52:00'`): PyYAML reads unquoted `52:00` as a base-60 number, Kronos as text. Output was verified byte-identical to the Python script on the real `health.md` and an edge-case fixture. The old PWA files (`manifest.json`, `sw.js`, `icon.svg`) are no longer written.

---

### Arthik inbox (`data/finance/inbox/`)

Kronos is one of Arthik's producers. `arthik_subscriptions` writes `kronos.md`; nothing else writes that file, and Arthik only reads it:

```yaml
updated: <ts>

last_run: '<YYYY-MM-DD>'               # days after this are checked on the next run
drafts:
  - id: sub-<slug(name)>-<YYYY-MM-DD>   # = the due event's id: stable, so nothing is drafted twice
    source: subscription
    date: '<YYYY-MM-DD>'
    title: <subscription name>
    type: expense
    amount: <as in subscription.md>
    account: <as in subscription.md, free text; Arthik maps it>
    category: <as in subscription.md, free text; Arthik maps it>
    notes: Subscription · <cycle>
```

A subscription is drafted when its `sub-…` event (created by `subscription_update`) is dated after the previous run (`last_run`, stored in `kronos.md`) and up to today, or when its `next_due_date` is today. The first run looks at today only, and a catch-up never goes back more than `max_catch_up_days`, so old due events (bills already handled, or from before the job existed) are never drafted. Drafts older than `keep_days` are dropped (Arthik keeps its own record in `drafts.md`). An empty list is written as `drafts: []`.

---

## 7. Outbox, and no locks

**Outbox** — producer side of Omnimo's "Outbox contract": `<dir>/<unix-ns>-<kind>.json`, written as `.json.tmp` then renamed. JSON v1: `{version:1, kind: notice|alert|briefing, source:"kronos", title?, text (trimmed, non-empty), created: RFC3339}`. Dir: `/data/notes/data/outbox` (= Omnimo's `outbox.dir`). Must be writable by UID 1000.

**No file locks.** By design any app may edit the data files at any time (Obsidian, Natlas, Omnimo, Kronos). Kronos reads, changes and atomically replaces a file within milliseconds; an edit saved by another app in that same instant could be overwritten — accepted trade-off. (Omnimo's `internal/filelock` is not used by Kronos.)

---

## 8. Adding a job

Go: `internal/jobs/<name>.go` with `func init() { job.Register("<name>", fn) }` + a `jobs:` entry with the same name + rebuild. Script: file in `./scripts` + `command:` entry + restart. Template: `archive_rollover.go`. Use `c.Path`, `c.Int/String/…` for options, `c.WriteFile/Move/Remove/AppendFile/MkdirAll` (dry-run aware), `c.Notify` for Telegram, `store.*` for YAML, `dates.*` for dates, pass `c` as the context to network calls. Return an error to FAIL + alert; log `Skip` and return nil for "nothing to do". `TestEveryBuiltInJobIsRegistered` lists the built-ins — update it.

---

## 9. Differences from Taskmaster (intended)

| Area | Taskmaster | Kronos |
|---|---|---|
| Telegram | own bot token, direct API | outbox → Omnimo (no token in Kronos) |
| Briefing AI | called old nimo:1418 | Omnimo writes the commentary |
| Month-end dates | drift (31 Jan → 28 Feb → 28 Mar) | `anchor_day` keeps them (31 Mar) |
| `once` / `normal` | `once` treated as unknown recurring rule → never became pending | read as `none` / `medium`, normalised on write |
| Missed runs | skipped | caught up once on start (48 h window) |
| Health site | 02:40 only | 02:45 + within ~20 s of a `health.md` edit |
| Alerts | "Exited with code 1" | the real error message |
| Schedule | 01:10–03:10 | 02:00–03:30 (+ briefing 05:40) |
| YAML formatting | PyYAML re-quoted everything, dropped comments | original quoting/comments kept, 2-space indent; content identical |
| Health page | 6-theme PWA (manifest, service worker, icon) | single nss-theme page, classless, no JS (since 2026-10-10) |
| Logs | `logs/taskmaster/task_YYYYMM.log` | `logs/kronos/kronos_YYYYMM.log` |
| New | — | `@manual`, `watch:`, `command:` jobs, `options:`, `--dry-run`, `list/next/health/notify` |

**Parity check (2026-10-05):** the same edge-case fixture set (overdue/pending/closed one-time, weekly/quarterly/daily/"2 weeks" recurring, closed early, unparseable date, missing priority, unicode/colon/apostrophe titles, flow-style records, extra fields; subscriptions due today/overdue/unknown cycle/cancelled/already-has-event; birthdays incl. 0000/29 Feb/bad data; health with malformed entries, links, floats, booleans, lists; inbox with CRLF/no-newline untitled files, excluded/dotfiles, name clashes) was run through every Python task and every Go job. All 16 outputs matched — YAML content and key order, `daily.md`, `note.md`, all four health-site files byte for byte, inbox/archive layout and contents, outbox messages. Fixtures avoided the intended differences above; those are covered by Go unit tests.

---

## 10. Tests

`go test ./...` (also in `docker build`): dates (Python-equal rules, anchor), store (error messages, round-trip order, PyStr/PyFloat/escape, UniquePath), outbox (contract), jobs (anchor drift, no anchor for ordinary days, legacy normalisation, broken file never overwritten, dry run writes nothing, missed-day subscription event + idempotency, New Year birthdays, partial agenda, registry), scheduler (ok/fail/panic/timeout, command env/exit/stderr/timeout, bad jobs skipped + alerted, catch-up order and rules, Prev, watch debounce).

---

## 11. Known limits

- Jobs are sequential; a slow job delays the next one (all current jobs take < 1 s; birthday_sync is bounded by a 60 s HTTP timeout).
- An abandoned (timed-out) Go job keeps running in the background until it returns.
- `event_archive` ages events by `date`, not close date (no `closed_on` field).
- Daemon crash count never resets.
- Catch-up only for jobs that have run at least once under Kronos (first deploy: no catch-up).
- Watch is polling (every tick), not inotify; fine for a handful of files.

---

## 12. How to ask for changes

Share this file plus only the file(s) being changed. Expect surgical snippets (add / replace / remove), not full rewrites.
