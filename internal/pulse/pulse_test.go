package pulse

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.com/niharokz/kronos/internal/dates"
)

func f(v float64) *float64 { return &v }

func day(s string) time.Time {
	t, _ := dates.ParseISO(s)
	return t
}

func TestParseDayKeepsMissingApart(t *testing.T) {
	d, err := ParseDay([]byte(`# Rahi daily log
date: '2026-10-23'
final: true
health_error: permission revoked
health: { steps: 8421, sleep: { min: 385 } }
screen: { total_min: 245, unlocks: 61, first_use: '07:08', last_use: '23:51' }
apps:
  - { pkg: com.instagram.android, name: Instagram, min: 52, opens: 9 }
mindful: { by_app: { com.instagram.android: { turned_back: 2 } } }
`))
	if err != nil {
		t.Fatal(err)
	}
	if d.Steps != nil || d.SleepMin != nil {
		t.Error("health_error day must count as missing health")
	}
	if *d.ScreenMin != 245 || *d.Unlocks != 61 || d.FirstUse != "07:08" || !d.Final || dates.Format(d.Date) != "2026-10-23" {
		t.Errorf("parsed %+v", d)
	}
	if v := d.Value(AppMetric("com.whatsapp")); v == nil || *v != 0 {
		t.Error("tracked app missing from apps: should be 0 when screen data exists")
	}
	if d.TurnedBack["com.instagram.android"] != 2 {
		t.Error("turned_back not read")
	}
	nulls, _ := ParseDay([]byte("date: 2026-10-24\nhealth: null\nscreen: null\n"))
	if nulls.Steps != nil || nulls.ScreenMin != nil || nulls.Value(AppMetric("x")) != nil {
		t.Error("null sections must stay missing, never 0")
	}
}

func TestMedianOverPattern(t *testing.T) {
	if *Median([]float64{5, 1, 3}) != 3 || *Median([]float64{1, 2, 3, 4}) != 2.5 || Median(nil) != nil {
		t.Error("median")
	}
	screen, steps := Metric{Key: KeyScreen}, Metric{Key: KeySteps, Higher: true}
	if !Over(screen, 126, 100) || Over(screen, 125, 100) || !Over(steps, 74, 100) || Over(steps, 75, 100) {
		t.Error("over thresholds")
	}
	if !Pattern([]bool{false, false, false, false, true, false, true}) || // 2 of last 3
		!Pattern([]bool{true, false, true, false, true, false, false}) || // 3 of 7
		Pattern([]bool{false, false, false, false, false, true, false}) {
		t.Error("pattern")
	}
}

func TestGoalAdjust(t *testing.T) {
	g, _ := ParseGoals([]byte("limits: { screen: { min: 120 }, steps: { max: 12000 } }\n"))
	screen, steps := Metric{Key: KeyScreen}, Metric{Key: KeySteps, Higher: true}

	st := &MetricState{Goal: &Goal{Start: 200, Current: 200}}
	st.AdjustWeek(g, screen, "2026-W43", f(190)) // met → tighten 5%
	if st.Goal.Current != 190 {
		t.Fatalf("tighten: %v", st.Goal.Current)
	}
	st.AdjustWeek(g, screen, "2026-W44", f(250)) // one miss: no change
	st.AdjustWeek(g, screen, "2026-W45", f(250)) // two misses → loosen, capped at start
	if st.Goal.Current != 199.5 || st.Goal.Missed != 0 {
		t.Fatalf("loosen: %+v", st.Goal)
	}
	floor := &MetricState{Goal: &Goal{Start: 125, Current: 125}}
	floor.AdjustWeek(g, screen, "2026-W43", f(100))
	if floor.Goal.Current != 120 {
		t.Errorf("clamp to hard min: %v", floor.Goal.Current)
	}
	top := &MetricState{Goal: &Goal{Start: 11800, Current: 11800}}
	top.AdjustWeek(g, steps, "2026-W43", f(13000))
	if top.Goal.Current != 12000 {
		t.Errorf("clamp to hard max: %v", top.Goal.Current)
	}
	fresh := &MetricState{Goal: &Goal{Start: 200, Current: 200, History: []GoalChange{{Week: "2026-W43", Change: "started"}}}}
	fresh.AdjustWeek(g, screen, "2026-W43", f(100))
	if fresh.Goal.Current != 200 {
		t.Error("a goal started this week must not move until next week")
	}
}

func TestTarget(t *testing.T) {
	g, _ := ParseGoals([]byte("{}"))
	screen := Metric{Key: KeyScreen}
	st := &MetricState{Weekday: Medians{M28: f(240)}, Goal: &Goal{Start: 240, Current: 230}}
	mon := day("2026-10-19")
	if v, _ := st.Target(g, screen, f(300), mon); v != 270 { // over baseline → yesterday −10%
		t.Errorf("target after bad day = %v", v)
	}
	if v, _ := st.Target(g, screen, f(250), mon); v != 230 { // −10% would be stricter than goal
		t.Errorf("never stricter than goal: %v", v)
	}
	if v, _ := st.Target(g, screen, f(200), mon); v != 230 {
		t.Errorf("good day → goal: %v", v)
	}
}

func TestStepsStillToCome(t *testing.T) {
	// Fallback: 8000 typical over 07:00–23:00, at 15:00 half the day remains.
	if v := StepsStillToCome("15:00", nil, 8000, "07:00", "23:00"); v != 4000 {
		t.Errorf("fallback = %v", v)
	}
	var hist []StepDay
	for i := 0; i < LearnAfter; i++ {
		hist = append(hist, StepDay{Total: 9000, Snaps: Intraday{Snapshots: []Snapshot{{At: "14:30", Steps: f(6000)}}}})
	}
	if v := StepsStillToCome("15:00", hist, 8000, "07:00", "23:00"); v != 3000 {
		t.Errorf("learned = %v", v)
	}
}

func TestPlanBudgetQuietDefer(t *testing.T) {
	today := day("2026-10-24")
	s := &Sent{}
	cands := []Message{
		{ID: "steps-soft-x", Type: TypeStepsSoft}, {ID: "app-x", Type: TypeApp},
		{ID: "pattern-x", Type: TypePattern}, {ID: "steps-final-x", Type: TypeStepsFinal},
	}
	send, def := Plan(cands, s, today, 2, false)
	if len(send) != 2 || send[0].ID != "pattern-x" || send[1].ID != "steps-final-x" || len(def) != 0 {
		t.Fatalf("priority: %+v %+v", send, def)
	}
	send, def = Plan([]Message{{ID: "pattern-y", Type: TypePattern}, {ID: "app-y", Type: TypeApp}}, s, today, 3, true)
	if len(send) != 0 || len(def) != 1 || def[0].Channel != ChanBriefing {
		t.Fatalf("quiet: %+v %+v", send, def)
	}
	s.Record(Message{ID: "app-x", Type: TypeApp}, today, "")
	if send, _ := Plan([]Message{{ID: "app-x", Type: TypeApp}}, s, today, 3, false); len(send) != 0 {
		t.Error("an id is never sent twice")
	}
}

func TestQuietWindowWraps(t *testing.T) {
	w := Window{Start: "22:30", End: "08:00"}
	if !w.Contains("23:00") || !w.Contains("07:59") || w.Contains("08:00") || w.Contains("12:00") {
		t.Error("wrap")
	}
}

func TestReadFeedbackAndBackOff(t *testing.T) {
	dir := t.TempDir()
	read := filepath.Join(dir, "inbox", "read")
	_ = os.MkdirAll(read, 0o755)
	os.WriteFile(filepath.Join(read, "nudge-insta-20261020_20261020.md"), []byte("---\nid: nudge-insta-20261020\nread_at: '2026-10-20 18:02'\ndismissed: true\n---\nx\n"), 0o644)
	os.WriteFile(filepath.Join(read, "nudge-insta-20261021_20261022-2.md"), []byte("# old file without fields\n"), 0o644)
	items := LoadRead(dir)
	byID := map[string]ReadItem{}
	for _, it := range items {
		byID[it.ID] = it
	}
	if !byID["nudge-insta-20261020"].Dismissed || dates.Format(byID["nudge-insta-20261021"].ReadDay) != "2026-10-22" {
		t.Fatalf("read items %+v", items)
	}

	m := AppMetric("com.instagram.android")
	s := &Sent{}
	days := map[string]Day{}
	for i, d := range []string{"2026-10-20", "2026-10-21", "2026-10-22"} {
		id := "n" + d
		s.Sent = append(s.Sent, SentItem{ID: id, Date: d, Type: TypeApp, Metric: m.Key, Channel: ChanInbox, Target: 30})
		items = append(items, ReadItem{ID: id, Dismissed: i == 0})
		days[d] = Day{Final: true, Apps: map[string]App{m.App: {Min: 60}}}
	}
	if !BackedOff(s, items, days, m, TypeApp, day("2026-10-23")) && !BackedOff(s, items, days, m, TypeApp, day("2026-10-24")) {
		t.Error("3 ignored nudges should back off (every other day)")
	}
	d := days["2026-10-22"]
	d.TurnedBack = map[string]float64{m.App: 1} // turning back counts as improvement
	days["2026-10-22"] = d
	if BackedOff(s, items, days, m, TypeApp, day("2026-10-23")) || BackedOff(s, items, days, m, TypeApp, day("2026-10-24")) {
		t.Error("a turn-back resets the back-off")
	}
}

func TestBuildRollupAndText(t *testing.T) {
	g, _ := ParseGoals([]byte("tracked_apps: [com.yt]\n"))
	st := &State{Metrics: map[string]*MetricState{KeyScreen: {Weekday: Medians{M28: f(200)}, Goal: &Goal{Current: 200}}}}
	var days []Day
	for i, v := range []float64{180, 260, 190} {
		days = append(days, Day{Date: day("2026-10-19").AddDate(0, 0, i), Final: true, ScreenMin: f(v), Unlocks: f(50),
			Apps: map[string]App{"com.yt": {Name: "YouTube", Min: 30}}})
	}
	r := BuildRollup("2026-W43", day("2026-10-19"), day("2026-10-25"), days, g, st)
	sc := r.Metrics[KeyScreen]
	if r.DaysLogged != 3 || *sc.Median != 190 || sc.Best.Value != 180 || sc.Worst.Value != 260 || *sc.GoalDays != 2 || r.Strikes[KeyScreen] != 1 {
		t.Fatalf("rollup %+v %+v", r, sc)
	}
	if len(r.Apps) != 1 || r.Apps[0].TotalMin != 90 || *r.Metrics[KeyUnlocks].Median != 50 {
		t.Errorf("apps/unlocks %+v", r)
	}
	text := ReportText(r, g, map[string]string{"com.yt": "YouTube"})
	if !strings.Contains(text, "Screen time: median 190 min") || !strings.Contains(text, "met 2/3 days") || !strings.Contains(text, "YouTube 90 min") {
		t.Errorf("report:\n%s", text)
	}
}

func TestSmallHelpers(t *testing.T) {
	if ISOWeek(day("2026-10-25")) != "2026-W43" || Slug("YouTube Music!") != "youtube-music" || Num(12345.6) != "12,346" ||
		MedianClock([]string{"07:00", "08:00", ""}) != "07:30" {
		t.Error("helpers")
	}
}
