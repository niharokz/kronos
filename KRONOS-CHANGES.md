# Kronos changes for Arthik 1.2.1

Copy these files into the Kronos repo (same paths), then add the kronos.yml lines below.
Tested against the current Kronos code: `go vet`, `go test ./...` all pass.

## Added

| File | What |
|---|---|
| `internal/jobs/arthik_subscriptions.go` | Job `arthik_subscriptions`: subscriptions that fell due since the last run (their `sub-<name>-<date>` event from `subscription_update`, or `next_due_date` = today) → `data/finance/inbox/kronos.md` for Arthik's inbox. Only Kronos writes that file. Ids = event ids, so nothing is drafted twice. Options: `max_catch_up_days` (3), `keep_days` (60). |
| `internal/jobs/arthik_test.go` | Tests for both jobs. |

## Changed

| File | Change |
|---|---|
| `internal/config/config.go` | one line in `defaultPaths`: `"finance_inbox": "/data/notes/data/finance/inbox",` |
| `kronos.yml.example` | the path and the two jobs below |
| `ARCHITECTURE.md`, `README.md` | job table rows + "Arthik inbox" contract section |

## Your kronos.yml (gitignored — edit by hand)

Under `paths:`:

```yaml
  finance_inbox: /data/notes/data/finance/inbox  # Arthik's inbox: Kronos writes kronos.md, reads drafts.md
```

Under `jobs:` (after `subscription_update`, and anywhere for the notice):

```yaml
  arthik_subscriptions:
    schedule: "12 2 * * *"
    description: "Hand due subscriptions to Arthik as drafts."

```


Kronos and Arthik both run as UID 1000 and share `/home/datar/data/notes/data/finance`,
so `inbox/` is writable by both (Kronos creates it if Arthik hasn't yet).

## Fix 2026-10-12 — stale subscription drafts

`arthik_subscriptions` used to look 7 days back on every run, so an old `sub-…` event
(e.g. Jio (Nihar) 2026-10-06, already renewed to 2027) became a draft days later.
It now remembers `last_run` in `kronos.md` and drafts only days after it (first run: today
only, at most `max_catch_up_days` after downtime). Replace `arthik_subscriptions.go` and
`arthik_test.go`; if your kronos.yml sets `lookback_days`, rename it to `max_catch_up_days`.
