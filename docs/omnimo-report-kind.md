# Omnimo: `report` outbox kind (for Kronos Pulse)

Kronos Pulse computes weekly and monthly rollups deterministically. Until Omnimo can turn them into prose, Kronos sends them as a plain-numbers `notice`. This is what Omnimo needs so that one AI call writes the report instead.

## Message

The same outbox contract (JSON v1, `.json.tmp` → rename), with a new kind:

```json
{
  "version": 1,
  "kind": "report",
  "source": "kronos",
  "title": "Pulse weekly 2026-W43",
  "text": "<the plain-numbers report — also the fallback>",
  "prompt": "pulse_weekly",
  "data": "<the rollup YAML, exactly as in logs/rahi/weekly/2026-W43.md>",
  "created": "2026-10-26T09:05:00+05:30"
}
```

- `prompt`: the name of a prompt in `nimo.yml` (`reports.pulse_weekly`, `reports.pulse_monthly`), so the wording lives with Nimo's persona.
- `data`: the computed rollup. The AI only writes prose from it; it must not invent numbers.

## Omnimo behaviour

1. Accept `kind: report` (unknown kinds keep going to `failed/`).
2. Make one AI call in background mode (`brain.Ask(..., ai.TierDeep, brain.ModeBackground)`), with system = `reports.<prompt>` and user = `data`.
3. Send `title` + the AI text. If the AI fails or times out, send `text` (the plain numbers), so a report is never lost.
4. Delete the file after sending. Never send both versions.

## Suggested `nimo.yml`

```yaml
reports:
  pulse_weekly: |
    You are Nimo. Write a short weekly check-in (max 8 lines) from this YAML rollup of
    screen time, app use, steps and sleep. Lead with the one thing that improved most,
    then the one to watch. Mention goal changes. Use only numbers present in the data.
  pulse_monthly: |
    Same as weekly, for a month: trends, best and worst week, goals reached.
```

## Kronos side once this exists

`internal/outbox` gets `Report = "report"` plus the two optional fields, and `pulse_rollup.sendReports` switches from `Notice` to `Report`, passing the rollup YAML as `data`. This is a one-line switch, and the plain-numbers `text` stays as the fallback.
