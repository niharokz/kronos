package jobs

import (
	"bytes"
	"context"
	"os"
	"testing"

	"gitlab.com/niharokz/kronos/internal/config"
	"gitlab.com/niharokz/kronos/internal/dates"
	"gitlab.com/niharokz/kronos/internal/job"
	"gitlab.com/niharokz/kronos/internal/store"
)

// Shared test helpers: a job context on a temp dir, file writing, event lookup.
// Each job's own tests live in <job>_test.go, next to <job>.go.

func testCtx(t *testing.T, today string, dry bool) (*job.Ctx, string, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.Parse([]byte(`paths:
  logs: `+dir+`/logs
  state: `+dir+`/state.json
  outbox: `+dir+`/outbox
  events: `+dir+`/event.md
  event_backup: `+dir+`/event.md.bck
  subscriptions: `+dir+`/subscription.md
  calendar: `+dir+`/calendar.md
  daily: `+dir+`/daily.md
`), "test.yml")
	if err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	c := job.NewCtx(context.Background(), cfg, config.Job{Name: "test"}, dry, out)
	c.Today, _ = dates.ParseISO(today)
	return c, dir, out
}

func put(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func eventByID(t *testing.T, path, id string) map[string]string {
	t.Helper()
	recs, err := store.LoadRecords(path, "events", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if store.Field(r, "id") == id {
			out := map[string]string{}
			for i := 0; i+1 < len(r.Content); i += 2 {
				out[r.Content[i].Value] = r.Content[i+1].Value
			}
			return out
		}
	}
	t.Fatalf("event %s not found", id)
	return nil
}
