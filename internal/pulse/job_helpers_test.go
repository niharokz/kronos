package pulse

import (
	"bytes"
	"context"
	"os"
	"testing"

	"gitlab.com/niharokz/kronos/internal/config"
	"gitlab.com/niharokz/kronos/internal/dates"
	"gitlab.com/niharokz/kronos/internal/job"
)

// Test helpers for the Pulse jobs: a job context on a temp dir, and file writing.

func testCtx(t *testing.T, today string, dry bool) (*job.Ctx, string, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.Parse([]byte(`paths:
  logs: `+dir+`/logs
  state: `+dir+`/state.json
  outbox: `+dir+`/outbox
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
