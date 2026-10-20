package jobs

import (
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func dailyList(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Content[0]
}

// Entries are sorted by date; undated or non-mapping entries are skipped.
func TestValidDailyEntries(t *testing.T) {
	c, _, _ := testCtx(t, "2026-10-20", false)
	got := validDailyEntries(c, dailyList(t, `
- {date: '2026-10-19', steps: 900}
- just text
- {steps: 5}
- {date: '2026-10-18', steps: 800}
`))
	if len(got) != 2 || dailyDate(got[0]).Day() != 18 || dailyDate(got[1]).Day() != 19 {
		t.Fatalf("got %d entries", len(got))
	}
}

// A card without values is skipped; headlines name the latest day.
func TestRenderDailyCards(t *testing.T) {
	c, _, _ := testCtx(t, "2026-10-20", false)
	html := renderDailyCards(validDailyEntries(c, dailyList(t, `
- {date: '2026-10-18', steps: 8000, hr_rest: 60, hr_avg: 75, hr_max: 140}
- {date: '2026-10-19', steps: 9000, hr_rest: 58, hr_avg: 72, hr_max: 150}
`)))
	for _, want := range []string{"<h3>Steps</h3>", "9000 steps on Oct 19", "<h3>Heart rate</h3>", "58 bpm resting on Oct 19"} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, skip := range []string{"<h3>Sleep</h3>", "<h3>Calories</h3>"} {
		if strings.Contains(html, skip) {
			t.Errorf("%s shown without data", skip)
		}
	}
}

// Only the latest dailyChartDays entries are charted.
func TestRenderDailyCardsWindow(t *testing.T) {
	c, _, _ := testCtx(t, "2026-12-31", false)
	var b strings.Builder
	for d := 1; d <= 40; d++ {
		fmt.Fprintf(&b, "- {date: '2026-11-%02d', steps: %d}\n", (d-1)%30+1, d)
	}
	entries := validDailyEntries(c, dailyList(t, b.String()))
	html := renderDailyCards(entries)
	if n := strings.Count(html, "<rect"); n != dailyChartDays {
		t.Errorf("bars = %d, want %d", n, dailyChartDays)
	}
}
