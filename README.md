# Kronos

A small, modular **job scheduler** for a self-hosted homelab — one static Go binary in one container.

- **One file per job.** A job is a Go function that registers itself; switch it on with one entry in `kronos.yml`.
- **Or just a script.** Any job can run a command instead; drop a script into `scripts/` and restart — no rebuild.
- **Plain-file data.** Jobs work on YAML-in-`.md` files, so the data stays human-editable (Obsidian-friendly).
- **Telegram without a token.** Messages are handed to a chat bot through a shared *outbox* folder ([Omnimo](https://gitlab.com/niharokz/omnimo) delivers them).
- **Safe by default.** Atomic writes, refusing to save empty lists, catch-up after downtime, timeouts, failure alerts, and `--dry-run` for every job.

---

## Contents

- [How it works](#how-it-works)
- [Included jobs](#included-jobs)
- [Data files](#data-files)
- [Pulse — digital + health coach](#pulse--digital--health-coach)
  - [Testing Pulse](#testing-pulse)
- [Setup](#setup)
- [Configuration](#configuration)
- [Adding a job](#adding-a-job)
- [Command line](#command-line)
- [Logging and alerts](#logging-and-alerts)
- [Design conventions](#design-conventions)
- [Project layout](#project-layout)

---

## How it works

`kronos serve` reads `kronos.yml` and runs each job on its schedule:

| `schedule:` | Runs |
|---|---|
| `"30 2 * * *"` | Standard 5-field cron, in the container's time zone (`TZ`). |
| `@startup` | Once, each time Kronos starts. |
| `@daemon` | Kept running; restarted 30 s after it exits; gives up after 5 crashes. |
| `@manual` | Only via `kronos run <job>` (or a watch, below). |

Any job can also have `watch: [files]`: it then runs again ~10–20 s after one of those files changes (debounced, so a burst of saves triggers one run).

**Execution model**

- Jobs run **one at a time**, in the order they become due; jobs due in the same minute run in `kronos.yml` order.
- A job's next run is computed from when it **finished**, so a long run skips a slot instead of piling up.
- **Catch-up:** if the host was off when a job was due, Kronos runs the missed slot once when it starts (within `catch_up_hours`, default 48), in slot order. Opt out per job with `catch_up: false`.
- Every run has a **timeout** (default 15 min). Go jobs are abandoned, command jobs are killed.
- A failing run (error, panic, timeout, non-zero exit) is logged as `FAIL` and sent as a Telegram **alert**.
- On stop, the running job finishes first (compose gives it `stop_grace_period`), then daemons are stopped.

---

## Included jobs

| Job | Default schedule | What it does |
|---|---|---|
| `restarted` | `@startup` | Telegram notice with time, host uptime (tells a host reboot from a container restart) and RAM. |
| `untitled_import` | daily 02:00 | Appends every `*untitled*` inbox file to `note.md` under a dated heading, then deletes the original. |
| `inbox_archive` | daily 02:05 | Moves inbox files not modified for 30 days to the archive, never overwriting (`todo.md` → `todo_1.md`). |
| `subscription_update` | daily 02:10 | Rolls overdue due dates forward by their cycle; a subscription due **today** gets a one-time `pending` event. |
| `arthik_subscriptions` | daily 02:12 | Subscriptions that fall due (since its last run, so never old ones) become drafts in Arthik's inbox (`data/finance/inbox/kronos.md`), to accept or reject in Arthik. |
| `event_archive` | daily 02:15 | Moves closed one-time events older than 30 days into a backup file. |
| `calendar_sync` | daily 02:20 | Copies every visible Google Calendar (your events, contact birthdays, holidays, shared calendars) into `calendar.md` — Calendar API, read-only, 7 days back to 365 ahead. |
| `event_update` | daily 02:30 | Overdue one-time events → today as `pending`; overdue recurring → next date; closed recurring → reopened at next date; sorts. |
| `health_website` | daily 02:45 + on edit | Renders `health.md` into one static page in the nss look: classless, no JavaScript, dark/light by system. Adds a stats strip, pace per run, countdowns and a Daily activity section (steps, sleep, heart rate, calories charts) from `daily:` entries. |
| `daily_update` | daily 03:00 | Writes the agenda: **Overdue**, **Due Today**, **Upcoming (7 days)** — events, subscriptions and `calendar.md` (lines start with the category, e.g. `BIRTHDAY- Asha`, `FESTIVAL- Karva Chauth`). |
| `archive_rollover` | 1 Jan 03:30 | Moves loose archive files into a folder named after the previous year. |
| `nimo_briefing` | daily 05:40 | Sends the agenda as a *briefing* — the bot posts it and adds an AI commentary. With Pulse set up, its brief (yesterday's verdict, today's targets) is appended. |
| `pulse_watch` | every 30 min, 08:00–22:30 | Pulse: snapshots today's Rahi log; nudges an app past its usual while on a strike pattern, and steps when you're unlikely to finish. |
| `pulse_rollup` | hourly at :05 | Pulse: for each Rahi day that turned final — baselines, self-tightening goals, pattern warnings, weekly/monthly rollups and reports; refreshes the briefing part. |

`kronos jobs` lists the built-in jobs; options per job are documented at the top of its file in `internal/jobs/`.

---

## Data files

All are YAML. List files start with an `updated: <timestamp>` line. **Unknown fields are kept** — other tools can add their own.

### `event.md`

```yaml
events:
  - id: renew-passport          # unique; auto-created ones look like sub-<name>-<date>
    title: Renew passport
    date: '2026-10-15'          # YYYY-MM-DD
    status: open                # open | pending | closed
    frequency: none             # none | daily | weekly | monthly | quarterly | yearly | "N days|weeks|months|years"
    priority: high              # high | medium | low
    last_done: '2026-09-15'     # set when a recurring event reopens
    anchor_day: 31              # set automatically for month-end items (see below)
```

- **open** – upcoming · **pending** – due/overdue, needs action · **closed** – done.
- Closing a recurring event — even early — reopens it at its **next** occurrence.
- `anchor_day` keeps a 29th–31st monthly/yearly item on its day: 31 Jan → 28 Feb → **31 Mar**. It is ignored if you move the date by hand.

### `subscription.md`

```yaml
subscriptions:
  - name: Music streaming
    status: active              # active | cancelled
    next_due_date: '2026-10-20'
    cycle: monthly              # same rules as frequency
    cost: 99                    # any extra fields are preserved
```

### `calendar.md` (generated by `calendar_sync`)

```yaml
type: calendar
version: 1
last_sync: '2026-10-17'
events:                         # one entry per day an event repeats, sorted by date, time, title
  - id: 7kq2…_20261020           # Google's event id (stable per repeat)
    title: Jane's birthday
    date: '2026-10-20'
    category: birthday          # one tag per entry — see the list below
    calendar: Personal
  - id: 3fh8…
    title: Dentist
    date: '2026-10-18'
    time: '10:30'               # only for timed events (container time zone)
    category: event
    calendar: Personal
    location: Main Street          # only when set
  - id: 9ab1…
    title: Goa trip
    date: '2026-10-24'
    end: '2026-10-26'           # only for multi-day events (last day, inclusive)
    category: event
    calendar: Personal
```

`category` comes from what Google says the entry is:

| `category` | From |
|---|---|
| `birthday` | contact and your own birthdays |
| `anniversary` | contact anniversaries |
| `holiday` | a holiday calendar's public holidays |
| `festival` | a holiday calendar's observances (festivals, days that aren't public holidays) |
| `reservation` | flights, hotels, tickets Google found in Gmail |
| `out_of_office` · `focus_time` · `working_location` | those Google Calendar entry types |
| `event` | everything else |
| *your own* | `options.categories` maps a calendar to a category, e.g. `{ Family: family, Cricket: sport }` — it wins over the above |

Rewritten on every sync (Google is the source of truth — edit events in Google Calendar). If Google fails or returns nothing, the old file is kept.

### `health.md`

```yaml
meta:
  title: Health Diary
  subtitle: Run • Lift • Improve
  tagline: Version 1 in progress     # optional second line under the title
  footer: Keep moving
  last_updated: 03 Oct 2026
  weight: 72kg
  height: 175cm
  body_fat: 18                       # BMI is calculated when not given
runs:
  planned:
    - name: City 10K
      date: 25 Oct 2026              # also 2026-10-25 or 25-10-2026; planned runs get a countdown
      distance: 10K
      link: example.com/race  bib 42 # makes the title clickable; text after the URL shows as "ref"
  recent:
    - name: Sunday long run
      date: 28 Sep 2026
      distance: 10K
      time: '58:30'                  # quote it; with distance gives pace /km
      cost: 900                      # 0 shows "Free"; summed in the stats strip
      reward: Medal                  # highlighted; any other key shows as a label/value pair
gym:
  - day: DAY 1
    title: Push
    exercises: [DB Bench Press 4x10, Dips]   # "4x10"-style sets are shown on the right
    full_width: false
daily:                               # one entry per day (e.g. from a wearable); every field but date optional
  - date: '2026-10-19'
    steps: 6020
    sleep_h: 7.8                     # plus deep_h, rem_h, light_h, awake_h for the stage chart
    hr_rest: 58                      # hr_avg, hr_max: three-line heart-rate chart
    kcal_total: 2190
    kcal_active: 410
```

The Daily activity section charts the latest 30 entries; a chart with no values is left out.

---

## Pulse — digital + health coach

Pulse reads the daily logs written by the **Rahi** launcher (`<rahi>/daily/YYYYMMDD.md`, never modified by Kronos) and coaches with deterministic rules — no AI decides anything.

| File (under `paths.rahi`) | Written by | What |
|---|---|---|
| `goals.md` | you (Kronos creates a commented default once) | hard limits, `tracked_apps`, quiet hours, daily message cap, step-nudge times |
| `baseline.md` | `pulse_rollup` | 7/28-day medians (weekday and weekend apart), goals with their history, what is rolled up |
| `state/intraday-YYYYMMDD.yml` | `pulse_watch` | one snapshot per run (kept 90 days) — the step forecast learns from them |
| `state/sent.yml` | both | every message: dedup, daily cap, feedback (90 days) |
| `state/brief.yml` | `pulse_rollup` | the Pulse part of today's morning briefing |
| `weekly/YYYY-Www.md`, `monthly/YYYYMM.md` | `pulse_rollup` | averages, medians, best/worst day, goal days, per-app totals, strikes, goal changes, unlocks |
| `inbox/<id>.md` | both | nudges and the short weekly report on Rahi's home screen |

**Rules**
- **Missing ≠ 0:** null values and `health_error` days are left out. Pulse only observes until 7 days are logged.
- **Goals:** screen time, each tracked app, steps, sleep. Each starts at your 28-day median. A week whose median meets the goal tightens it 5%; two missed weeks in a row loosen it 5% (never past the start). Goals always stay within `limits`.
- **Today's target:** after a day worse than your baseline, yesterday ∓10% — never stricter than the goal.
- **Strikes:** a day is *over* at >125% of baseline (<75% for steps and sleep). One over day is mentioned in the briefing; 2 of the last 3 or 3 of the last 7 is a Telegram warning (once per metric per 3 days).
- **Nudges** (Rahi inbox):
  - an app past its 7-day median while on a strike pattern — once per app per day;
  - steps when you're unlikely to finish — at most a soft and a final nudge, silent once reached. The forecast spreads your usual total over waking hours until 14 goal days of snapshots exist, then uses what you typically walk after that time of day.
- **Budget:** `max_messages_per_day` (reports excluded) and no messages in quiet hours. A pattern warning is never dropped; it moves to the next briefing.
- **Feedback:** if the last 3 nudges of a kind were dismissed (✓ without opening) or the day still ended off target, that kind backs off to every other day for a week. A mindful turn-back counts as improvement.
- **Reports:** weekly (when Sunday is final) to Telegram plus a short version in the inbox; monthly to Telegram. They are plain numbers until Omnimo can write prose from the rollup (see `docs/omnimo-report-kind.md`).

Message ids are deterministic (`nudge-instagram-20261024`, `steps-final-20261024`, `pattern-screen-time-20261023`, `report-weekly-2026-W43`), so reruns never duplicate. A log Rahi is still writing is skipped with a warning and read again on the next run.

### Testing Pulse

Build a sandbox of fake Rahi logs, then run the Pulse jobs against it at any time of day. Your vault, Telegram, `paths.logs` and Kronos state are never touched. Inside the container use `docker exec kronos kronos …` and a folder under `/tmp` (a 16 MB scratch space, cleared on restart).

**1. Fake logs** — 35 final days before `--today`, plus intraday step snapshots (every 30 min, 08:00–22:30) for each day with health data, and a `goals.md` tracking Instagram and YouTube:

```sh
kronos pulse-fixture --out /tmp/pulse --today 2026-11-30 \
  --today-at 19:00 --today-steps 3000 \
  --heavy 10 \
  --streak com.google.android.youtube:3 \
  --null-health 15,16 --health-error 20 \
  --missing-app com.instagram.android:5
```

| Option | Pattern |
|---|---|
| `--days N` | number of final days (default 35); weekdays and weekends differ |
| `--today-at HH:MM`, `--today-steps N` | a partial (non-final) log for `--today`, for `pulse_watch` |
| `--heavy N` | one-off heavy day N days ago: lots of Instagram/YouTube, few steps, short sleep |
| `--streak pkg:N` | the app well over its usual for the last N days (continues in today's partial log) |
| `--null-health 15,16` | `health: null` on those days (missing, never 0) |
| `--health-error 20` | `health_error` set; the stale health values must be ignored |
| `--missing-app pkg:N` | the app absent from `apps:` for the last N days (counts as 0) |
| `--tracked a,b`, `--seed N`, `--force` | tracked apps, another random variation (same seed = same logs), replace an earlier fixture |

Day numbers count back from `--today` (1 = yesterday). `--force` only wipes a folder that has the fixture's `.pulse-fixture` marker, never your vault.

**2. Run the jobs** with a fake clock:

```sh
# morning rollup: baselines, goals, weekly/monthly, pattern warnings, brief
kronos run pulse_rollup --rahi-path /tmp/pulse --now '2026-11-30 09:05' --dry-run

# nudges through the day: soft steps after 16:00, final after 20:30, quiet from 22:30
kronos run pulse_watch --rahi-path /tmp/pulse --now '2026-11-30 16:30' --dry-run
kronos run pulse_watch --rahi-path /tmp/pulse --now '2026-11-30 20:45' --dry-run
kronos run pulse_watch --rahi-path /tmp/pulse --now '2026-11-30 23:00' --dry-run
```

| Flag | Effect |
|---|---|
| `--rahi-path DIR` | read and write the sandbox instead of `paths.rahi` (pulse_* jobs only). Telegram messages go to `DIR/_outbox/`, logs to the screen only |
| `--now 'YYYY-MM-DD HH:MM'` | pretend it is that local time — for quiet hours, step-nudge times and `briefing_at`. On real data it needs `--dry-run` |
| `--dry-run` | print each message instead of sending it: `~ would send [p1] inbox steps-final-20261130 — title`, then the text. `[p0]`…`[p3]` is the priority (pattern, steps-final, app, steps-soft); `[report]` is outside the daily cap |

In a sandbox, a dry run still writes `baseline.md`, `weekly/`, `monthly/`, `state/` (snapshots, `brief.yml`, `sent.yml`), so later runs see earlier ones — dedup, the daily cap and back-off behave as they would for real. Without `--dry-run` the inbox files are written too (`DIR/inbox/`), and Telegram notices land in `DIR/_outbox/`. Start over with `--force` or by deleting the folder.

Check the results:

```sh
cat /tmp/pulse/baseline.md /tmp/pulse/state/brief.yml
ls /tmp/pulse/weekly /tmp/pulse/monthly /tmp/pulse/inbox
```

---

## Setup

1. **Clone and configure**

   ```bash
   git clone https://gitlab.com/niharokz/kronos.git && cd kronos
   cp kronos.yml.example kronos.yml        # schedules and container paths
   cp .env.example .env                    # host folders, user, time zone
   ```

2. **Host settings in `.env`** (gitignored; `docker compose` reads it for `docker-compose.yml`):

   | Variable | Meaning |
   |---|---|
   | `DATA_PATH` | Base data folder on the host (only used to build the paths below) |
   | `NOTE_PATH` | Host folder of the notes vault, e.g. `${DATA_PATH}/notes` → mounted at `/data/notes` |
   | `WEB_PATH` | Host folder the health site is published under → mounted at `/docker/web` |
   | `PUID`, `PGID` | Owner of the files Kronos writes (`id -u`, `id -g`) |
   | `TZ` | Time zone for the cron schedules |

   Nothing host-specific lives in the repo: `docker-compose.yml` only references these variables, and `kronos.yml` uses container paths. Compose refuses to start if `NOTE_PATH` or `WEB_PATH` is unset. The host folders must be writable by `PUID:PGID`.

3. **Telegram (optional).** Kronos writes messages into `paths.outbox`; a bot that reads that folder sends them. With Omnimo, set its `outbox.dir` to the same folder. Without a bot, messages simply stay in the folder.

4. **Google Calendar (optional, for `calendar_sync`).** Enable the **Google Calendar API**, create an OAuth client of type *Desktop app*, then run once on a machine with a browser:

   ```python
   # pip install google-auth-oauthlib
   from google_auth_oauthlib.flow import InstalledAppFlow
   flow = InstalledAppFlow.from_client_secrets_file(
       "credentials.json", ["https://www.googleapis.com/auth/calendar.readonly"])
   open("token.json", "w").write(flow.run_local_server(port=0).to_json())
   ```

   Put `token.json` in `./config/` (writable by the container user). Every calendar shown in Google Calendar is synced — add *Holidays in …* under *Other calendars* to get holidays; contact birthdays come with your own calendar. Kronos refreshes it automatically. Keep `credentials.json` and `token.json` out of git — both are ignored.
   > In *Testing* mode Google expires refresh tokens after 7 days — publish the app (it can stay unverified) to keep it valid. A log line with `invalid_grant` means the token must be regenerated.

5. **Run**

   ```bash
   docker compose up -d --build
   docker logs -f kronos
   ```

---

## Configuration

```yaml
paths:                        # every path a job uses; also exported to scripts as KRONOS_<NAME>
  events: /data/notes/data/event.md
  outbox: /data/notes/data/outbox
  # …

settings:
  tick_seconds: 10            # scheduler resolution
  timeout_seconds: 900        # default per-job timeout
  catch_up_hours: 48          # missed slots within this window run once on start
  alerts: true                # failures → Telegram

jobs:
  event_update:
    schedule: "30 2 * * *"
    description: "Update events."
    timeout: 300              # optional per-job override
    catch_up: true            # optional (default true)
    enabled: true             # optional (default true)
    watch: [/data/notes/data/event.md]   # optional
    options: { purge_after_days: 365 }  # job-specific settings
```

Config changes apply on `docker compose restart kronos`. Code changes need `docker compose up -d --build`.

---

## Adding a job

### A Go job — one file + one config entry

```go
// internal/jobs/water_reminder.go
package jobs

import (
	"gitlab.com/niharokz/kronos/internal/job"
	"gitlab.com/niharokz/kronos/internal/outbox"
)

func init() { job.Register("water_reminder", waterReminder) }

// waterReminder sends a Telegram nudge.
func waterReminder(c *job.Ctx) error {
	if err := c.Notify(outbox.Notice, "", c.String("text", "💧 Drink water")); err != nil {
		return err // any error → FAIL log + alert
	}
	c.Log.OK("Reminder sent")
	return nil
}
```

```yaml
# kronos.yml.example and your kronos.yml
  water_reminder:
    schedule: "0 11,16 * * *"
    description: "Water reminder."
    options: { text: "💧 Drink water" }
```

Put its tests in `internal/jobs/water_reminder_test.go`, then `docker compose up -d --build`. Every job gets a `*job.Ctx` with:

| | |
|---|---|
| `c.Log.Info/OK/Skip/Warn(…)` | Log lines (stdout + monthly log file) |
| `c.Path("events")` | A path from `paths:` |
| `c.Int / String / Bool / Strings(key, default)` | This job's `options:` |
| `c.Today`, `c.Now` | Today's date and the run's start time |
| `c.WriteFile`, `c.AppendFile`, `c.Move`, `c.Remove`, `c.MkdirAll` | File changes — atomic, and printed instead of done under `--dry-run` |
| `c.Notify(kind, title, text)` | Telegram via the outbox (`notice`, `alert`, `briefing`) |
| `c` itself | A `context.Context` carrying the timeout — pass it to HTTP calls |

`internal/store` has the YAML helpers the built-in jobs use (`LoadRecords`, `RecordsYAML`, `Get/Set` …), and `internal/dates` the date rules.

### Removing a job

Delete `internal/jobs/<name>.go` and `<name>_test.go`, remove its entry from `kronos.yml.example` and your `kronos.yml`, and rebuild. `go test` (also run by `docker build`) fails if a job is registered but missing from `kronos.yml.example`, or the other way round — so a half-removed job never ships.

### A script job — no rebuild

```yaml
  disk_check:
    schedule: "0 4 * * *"
    description: "Warn when the disk is over 90% full."
    command: ["sh", "/app/scripts/disk_check.sh"]
```

Scripts receive `KRONOS_<PATH>` for every path, `KRONOS_JOB` and `KRONOS_DRY_RUN`; a non-zero exit marks the run `FAIL` (the last stderr lines go into the alert). Send Telegram from a script with `kronos notify "text"`. See `scripts/disk_check.sh.example`.

---

## Command line

Run inside the container (`docker exec kronos kronos …`):

```
kronos run <job> [--dry-run]    run a job now; --dry-run prints the diff and changes nothing
     [--now 'YYYY-MM-DD HH:MM'] [--rahi-path DIR]   test Pulse (see Testing Pulse)
kronos pulse-fixture --out DIR  fake Rahi logs for testing Pulse (--help for patterns)
kronos list                     jobs, schedule, next run, last run, status
kronos next [n]                 the next n scheduled runs
kronos health                   PASS/FAIL per job (failed or missed run); exit 1 on FAIL
kronos jobs                     built-in Go jobs
kronos notify [--kind alert] [--title T] "text"   send a Telegram message (or pipe text in)
kronos version
```

---

## Logging and alerts

Every line goes to `docker logs` and to `<paths.logs>/kronos_YYYYMM.log`:

```
[2026-10-06 02:30:00] [START] [event_update] Update recurring events, overdue events, sorting and closed items.
[2026-10-06 02:30:00] [INFO ] [event_update] Loaded 42 events
[2026-10-06 02:30:00] [ OK  ] [event_update] Saved 42 events
[2026-10-06 02:30:00] [ OK  ] [event_update] Completed
```

Levels: `START INFO OK SKIP WARN FAIL DAEMN`. Every `FAIL` is also queued as a Telegram alert with the real error message. Last-run state is kept in `paths.state` (used by catch-up, `list` and `health`).

---

## Design conventions

- **Safe YAML only** — records are YAML nodes, never built from strings; unknown fields and their order survive.
- **Atomic writes** — temp file + fsync + rename; readers never see half a file.
- **No locks** — any app (Obsidian, the web UI, the bot, Kronos) can edit the data files at any time. Each writer reads, changes and atomically replaces the file within milliseconds.
- **Never write an empty list**, and **never treat an unreadable file as empty**.
- **Deterministic IDs** for anything auto-created, so reruns never duplicate.
- **Copy before remove** — destination first, then source: a crash can duplicate, never lose.
- **No secrets in the repo or the image** — `.env`, `kronos.yml`, `config/`, `scripts/` and any OAuth JSON are gitignored and dockerignored.
- **Least privilege** — non-root user, read-only image filesystem, all Linux capabilities dropped, `no-new-privileges`, no open ports.
- **Strict config** — an unknown key in `kronos.yml` (top level, `settings:` or a job) is an error, so typos never pass silently.

---

## Project layout

```
kronos/
├── cmd/kronos/                 CLI: serve, run, list, next, health, notify, pulse-fixture
├── internal/
│   ├── config/                 kronos.yml
│   ├── scheduler/              cron, startup, daemons, watches, catch-up, timeouts, alerts
│   ├── job/                    Register(), Ctx, logger, dry-run diff  ← the plug-in API
│   ├── jobs/                   built-in jobs: <name>.go + <name>_test.go each (+ assets/health.css)
│   ├── pulse/                  Pulse rules (baselines, goals, strikes, forecast, messages, rollups)
│   │   └── fixture/            fake Rahi logs for testing (kronos pulse-fixture)
│   ├── store/                  YAML records, atomic writes, Python-compatible formatting
│   ├── dates/                  date parsing and repeat rules
│   ├── outbox/                 Telegram hand-off (outbox folder)
│   └── state/                  last run per job
├── scripts/                    your script jobs (gitignored; one example)
├── config/                     Google token.json (gitignored)
├── kronos.yml.example
├── Dockerfile
├── docker-compose.yml          host values from .env
├── .env.example
└── LICENSE                     MIT
```

Tests: `go test ./...` (also run during `docker build`).

---

## License

[MIT](LICENSE) © 2026 niharokz
