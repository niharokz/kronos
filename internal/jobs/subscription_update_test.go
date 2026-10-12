package jobs

import (
	"path/filepath"
	"testing"

	"gitlab.com/niharokz/kronos/internal/store"
)

func TestMissedDayStillCreatesSubscriptionEvent(t *testing.T) {
	// Host off on the due day (Oct 3); catch-up on Oct 5 for a 2-day cycle lands on today.
	c, dir, _ := testCtx(t, "2026-10-05", false)
	put(t, filepath.Join(dir, "event.md"), "events:\n- {id: x, title: X, date: '2026-12-01', status: open, frequency: none, priority: low}\n")
	put(t, filepath.Join(dir, "subscription.md"), "subscriptions:\n- {name: Pills Box, status: active, next_due_date: '2026-10-03', cycle: 2 days}\n")
	if err := subscriptionUpdate(c); err != nil {
		t.Fatal(err)
	}
	e := eventByID(t, filepath.Join(dir, "event.md"), "sub-pills-box-2026-10-05")
	if e["status"] != "pending" || e["title"] != "Subscription due: Pills Box" {
		t.Errorf("got %v", e)
	}
	// Running again must not duplicate it.
	if err := subscriptionUpdate(c); err != nil {
		t.Fatal(err)
	}
	recs, _ := store.LoadRecords(filepath.Join(dir, "event.md"), "events", true)
	if len(recs) != 2 {
		t.Errorf("events = %d, want 2 (no duplicate)", len(recs))
	}
}
