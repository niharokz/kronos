package jobs

import (
	"os"
	"strings"
	"testing"

	"gitlab.com/niharokz/kronos/internal/store"
)

const subsFixture = `subscriptions:
  - id: elcty-bangalore
    name: Elcty (Bangalore)
    category: utility
    amount: 800.0
    cycle: monthly
    next_due_date: '2026-11-14'
    account: Amazon Pay ICICI Credit Card
    status: active
  - name: Jio Fibrenet
    category: utility
    amount: 707.82
    cycle: monthly
    next_due_date: '2026-10-14'
    account: Amazon Pay ICICI Credit Card
    status: active
  - name: Netflix
    amount: 199.0
    cycle: monthly
    next_due_date: '2026-10-14'
    status: cancelled
`

// Only subscriptions due since the last run become drafts: an old due event (from
// before the job existed, or a bill already handled) is never picked up.
func TestArthikSubscriptions(t *testing.T) {
	c, dir, _ := testCtx(t, "2026-10-14", false)
	c.Cfg.Paths["finance_inbox"] = dir + "/finance/inbox"
	out := dir + "/finance/inbox/kronos.md"
	ids := func() string {
		t.Helper()
		root, err := store.LoadMapping(out, true)
		if err != nil {
			t.Fatal(err)
		}
		recs, _ := store.RecordList(store.Get(root, "drafts"), "kronos.md", "drafts")
		var got []string
		for _, r := range recs {
			got = append(got, store.Field(r, "id"))
		}
		return strings.Join(got, ",")
	}
	put(t, dir+"/subscription.md", subsFixture)
	put(t, dir+"/event.md", `events:
  - {id: sub-elcty--bangalore-2026-10-12, title: old due event, date: '2026-10-12', status: pending, frequency: none}
  - {id: gold-purchase, title: Gold, date: '2026-10-14', status: open, frequency: none}
`)
	// First run: today only. Jio Fibrenet is due today; the 12 Oct event is older.
	if err := arthikSubscriptions(c); err != nil {
		t.Fatal(err)
	}
	if got := ids(); got != "sub-jio-fibrenet-2026-10-14" {
		t.Fatalf("first run drafts %q", got)
	}
	recs, _ := store.LoadRecords(out, "drafts", true)
	d := recs[0]
	if store.Field(d, "amount") != "707.82" || store.Field(d, "account") != "Amazon Pay ICICI Credit Card" ||
		store.Field(d, "date") != "2026-10-14" || store.Field(d, "category") != "utility" || store.Field(d, "title") != "Jio Fibrenet" {
		t.Fatalf("draft %v", d.Content)
	}
	before, _ := os.ReadFile(out)
	if err := arthikSubscriptions(c); err != nil { // same day again: nothing changes
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(out); string(after) != string(before) {
		t.Fatal("rerun rewrote kronos.md")
	}
	// Kronos was down on the 15th; on the 16th it catches up the 15th's due event.
	c.Today = c.Today.AddDate(0, 0, 2)
	put(t, dir+"/event.md", `events:
  - {id: sub-elcty--bangalore-2026-10-12, title: old, date: '2026-10-12', status: pending, frequency: none}
  - {id: sub-elcty--bangalore-2026-10-15, title: due, date: '2026-10-15', status: pending, frequency: none}
`)
	if err := arthikSubscriptions(c); err != nil {
		t.Fatal(err)
	}
	if got := ids(); got != "sub-jio-fibrenet-2026-10-14,sub-elcty--bangalore-2026-10-15" {
		t.Fatalf("catch-up drafts %q", got)
	}
	// Much later: everything is pruned, the file stays valid.
	c.Today = c.Today.AddDate(0, 0, 90)
	put(t, dir+"/event.md", "events: []\n")
	if err := arthikSubscriptions(c); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); !strings.Contains(string(b), "drafts: []") || !strings.Contains(string(b), "last_run: '2027-01-14'") {
		t.Fatalf("not pruned:\n%s", b)
	}
}
