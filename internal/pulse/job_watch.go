package pulse

import (
	"fmt"
	"time"

	"gitlab.com/niharokz/kronos/internal/dates"
	"gitlab.com/niharokz/kronos/internal/job"
)

// pulse_watch snapshots today's (partial) phone log every run and sends nudges:
// an app past its 7-day median while on a strike pattern (once per app per day),
// and steps when you're unlikely to reach today's target (soft, then final; max 2).
// Observe-only until 7 days of data. Quiet hours and the daily cap come from goals.md.

func init() { job.Register("pulse_watch", pulseWatch) }

func pulseWatch(c *job.Ctx) error {
	dirs := jobDirs(c)
	dir := dirs.Home // goals, baseline, state, rollups
	g, err := pulseGoals(c, dir)
	if err != nil { // your file: warn and retry, no hourly alert storm
		c.Log.Warn("goals.md: %v – fix the YAML; retried next run", err)
		return nil
	}
	today, hhmm := c.Today, c.Now.Format("15:04")
	day, ok, err := LoadDay(dirs.Day(today))
	if err != nil {
		c.Log.Warn("%v", err)
		return nil
	}
	if !ok {
		c.Log.Skip("No phone log for today yet")
		return nil
	}

	// Snapshot first: it feeds the step forecast even while observing.
	snap := Snapshot{At: hhmm, Steps: day.Steps, ScreenMin: day.ScreenMin, Apps: map[string]float64{}}
	for _, pkg := range g.TrackedApps {
		if v := day.Value(AppMetric(pkg)); v != nil {
			snap.Apps[pkg] = *v
		}
	}
	inPath := IntradayFile(dir, today)
	in, err := LoadIntraday(inPath)
	if err != nil {
		c.Log.Warn("%s unreadable, starting it again: %v", inPath, err)
		in = Intraday{}
	}
	in.Date = dates.Format(today)
	in.Add(snap)
	if err := pulseSave(c, inPath, "Pulse intraday snapshots (Kronos pulse_watch)", in); err != nil {
		return err
	}

	st, err := LoadState(BaselineFile(dir))
	if err != nil {
		c.Log.Warn("baseline.md: %v – retried next run", err)
		return nil
	}
	if !st.Ready() {
		c.Log.Skip("Observing: %d of %d days logged", st.DaysWithData, MinDays)
		return nil
	}
	sent, err := LoadSent(SentFile(dir))
	if err != nil {
		c.Log.Warn("sent.yml: %v – retried next run", err)
		return nil
	}
	logs, err := LoadLogs(dirs, today.AddDate(0, 0, -60), today.AddDate(0, 0, -1))
	if err != nil {
		return err
	}
	for _, p := range logs.Problems {
		c.Log.Warn("%s", p)
	}
	read := LoadRead(dirs.Inbox, dirs.InboxOld)
	yesterday, haveY := logs.ByDate[dates.Format(today.AddDate(0, 0, -1))]

	var cands []Message
	cands = append(cands, appNudges(g, st, sent, read, logs, day, today)...)
	if m, ok := stepNudge(c, dir, g, st, sent, read, logs, day, yesterday, haveY, hhmm); ok {
		cands = append(cands, m)
	}

	send, _ := Plan(cands, sent, today, g.MaxMessages, g.Quiet(hhmm))
	for _, m := range send {
		if err := pulseDeliver(c, m, sent, ""); err != nil {
			return err
		}
	}
	sent.Prune(today, 90)
	if err := pulseSave(c, SentFile(dir), "Pulse sent messages (Kronos)", sent); err != nil {
		return err
	}
	c.Log.OK("Snapshot %s; %d candidate(s), %d sent", hhmm, len(cands), len(send))
	return nil
}

// appNudges: tracked apps past their 7-day median today while on a strike pattern.
func appNudges(g Goals, st *State, sent *Sent, read []ReadItem, logs Logs, day Day, today time.Time) []Message {
	var out []Message
	names := logs.AppNames()
	for _, pkg := range g.TrackedApps {
		m := AppMetric(pkg)
		ms := st.Metrics[m.Key]
		v := day.Value(m)
		if ms == nil || v == nil {
			continue
		}
		recent := ms.Recent(today)
		overs := Overs(logs.ByDate, m, ms, today.AddDate(0, 0, -1))
		if recent == nil || *v <= *recent || !Pattern(overs) {
			continue
		}
		if BackedOff(sent, read, logs.ByDate, m, TypeApp, today) {
			continue
		}
		name := day.AppName(pkg)
		if n := names[pkg]; name == pkg && n != "" {
			name = n
		}
		target := *recent
		if ms.Goal != nil {
			target = ms.Goal.Current
		}
		out = append(out, Message{
			ID:      "nudge-" + Slug(name) + "-" + day8(today),
			Type:    TypeApp,
			Metric:  m.Key,
			Channel: ChanInbox,
			Title:   fmt.Sprintf("%s — %s min today", name, Num(*v)),
			Text: fmt.Sprintf("Past your usual %s min, and over on %d of the last 7 days. Goal: %s min.",
				Num(*recent), OverCount(overs), Num(target)),
			Target: target,
		})
	}
	return out
}

// stepNudge: steps unlikely to reach today's target — soft after step_nudges.soft, final after .final.
func stepNudge(c *job.Ctx, dir string, g Goals, st *State, sent *Sent, read []ReadItem,
	logs Logs, day, yesterday Day, haveY bool, hhmm string) (Message, bool) {
	m := Metric{Key: KeySteps, Higher: true}
	ms := st.Metrics[m.Key]
	today := c.Today
	if ms == nil || day.Steps == nil || hhmm < g.StepNudges.Soft {
		return Message{}, false
	}
	var yv *float64
	if haveY {
		yv = yesterday.Value(m)
	}
	target, ok := ms.Target(g, m, yv, yesterday.Date)
	if !ok || *day.Steps >= target { // silent once the goal is hit
		return Message{}, false
	}
	final := hhmm >= g.StepNudges.Final
	typ := TypeStepsSoft
	if final {
		typ = TypeStepsFinal
	}
	if sent.CountType(today, typ, m.Key) > 0 || (!final && sent.CountType(today, TypeStepsFinal, m.Key) > 0) {
		return Message{}, false
	}
	if BackedOff(sent, read, logs.ByDate, m, typ, today) {
		return Message{}, false
	}

	// Learn from goal days' snapshots; until then spread the typical total over waking hours.
	var history []StepDay
	var wakes, sleeps []string
	for _, d := range logs.Final() {
		wakes, sleeps = append(wakes, d.FirstUse), append(sleeps, d.LastUse)
		if d.Steps == nil || (ms.Goal != nil && *d.Steps < ms.Goal.Current) {
			continue
		}
		if in, err := LoadIntraday(IntradayFile(dir, d.Date)); err == nil && len(in.Snapshots) > 0 {
			history = append(history, StepDay{Total: *d.Steps, Snaps: in})
		}
	}
	typical := target
	if b := ms.Baseline(today); b != nil {
		typical = *b
	}
	still := StepsStillToCome(hhmm, history, typical, MedianClock(wakes), MedianClock(sleeps))
	if *day.Steps+still >= target {
		return Message{}, false
	}
	left := target - *day.Steps
	title := "Steps: " + Num(left) + " to go"
	if final {
		title = "Last call: " + Num(left) + " steps to go"
	}
	return Message{
		ID:      "steps-" + map[bool]string{false: "soft", true: "final"}[final] + "-" + day8(today),
		Type:    typ,
		Metric:  m.Key,
		Channel: ChanInbox,
		Title:   title,
		Text: fmt.Sprintf("%s steps so far, target %s. At your usual pace you'd end near %s — a %d-minute walk closes the gap.",
			Num(*day.Steps), Num(target), Num(*day.Steps+still), int(left/100)+1),
		Target: target,
	}, true
}
