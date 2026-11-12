package kshan

import (
	"bytes"
	"context"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.com/niharokz/kronos/internal/config"
	"gitlab.com/niharokz/kronos/internal/dates"
	"gitlab.com/niharokz/kronos/internal/job"
)

var update = flag.Bool("update", false, "rewrite golden files")

// testCtx: a job context on a temp dir (vault, state, outbox, events) with these options.
func testCtx(t *testing.T, name string, options map[string]any, dry bool) (*job.Ctx, string, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.Parse([]byte(`paths:
  logs: `+dir+`/logs
  state: `+dir+`/kronos/state.json
  outbox: `+dir+`/outbox
  vault: `+dir+`/vault
  events: `+dir+`/vault/data/event.md
  event_backup: `+dir+`/vault/data/event.md.bck
  kshan_events: `+dir+`/vault/logs/kshan/events.md
  kshan_events_old: `+dir+`/vault/logs/rahi/events.md
`), "test.yml")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(dir, "kronos"), 0o755)
	out := &bytes.Buffer{}
	c := job.NewCtx(context.Background(), cfg, config.Job{Name: name, Options: options}, dry, out)
	c.Now = time.Date(2026, 11, 12, 21, 20, 0, 0, time.Local)
	c.Today = dates.Today(c.Now)
	return c, dir, out
}

func put(t *testing.T, path, body string) {
	t.Helper()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// meteo serves testdata/forecast.json, or an error status.
func meteo(t *testing.T, status *int) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile("testdata/forecast.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("latitude") != "12.925" || q.Get("timezone") != "Asia/Kolkata" || q.Get("forecast_days") != "2" ||
			!strings.Contains(q.Get("current"), "relative_humidity_2m") {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		if *status != 200 {
			w.WriteHeader(*status)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The file matches the golden copy: quoted times, 1 decimal, nulls kept, the next whole hours only.
func TestWeatherGolden(t *testing.T) {
	status := 200
	srv := meteo(t, &status)
	c, dir, out := testCtx(t, "kshan_weather", map[string]any{"url": srv.URL}, false)
	if err := kshanWeather(c); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "vault", "logs", "kshan", "weather.md"))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	golden := "testdata/weather.golden"
	if *update {
		_ = os.WriteFile(golden, got, 0o644)
	}
	want, _ := os.ReadFile(golden)
	if string(got) != string(want) {
		t.Errorf("weather.md differs from %s:\n%s", golden, got)
	}
}

// Options: place, hours, an absolute file.
func TestWeatherOptions(t *testing.T) {
	status := 200
	srv := meteo(t, &status)
	file := filepath.Join(t.TempDir(), "w.md")
	c, _, _ := testCtx(t, "kshan_weather", map[string]any{"url": srv.URL, "place": "Home", "hours": 2, "file": file}, false)
	if err := kshanWeather(c); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(file)
	s := string(raw)
	if !strings.Contains(s, "place: Home\n") || strings.Count(s, "  - {time:") != 2 || !strings.Contains(s, "# the next 2 whole hours") {
		t.Errorf("options ignored:\n%s", s)
	}
}

// A failed fetch keeps the old file and only warns; after fail_hours it alerts once, then warns again;
// the next success clears the mark.
func TestWeatherFailures(t *testing.T) {
	status := 503
	srv := meteo(t, &status)
	c, dir, out := testCtx(t, "kshan_weather", map[string]any{"url": srv.URL}, false)
	file := filepath.Join(dir, "vault", "logs", "kshan", "weather.md")
	put(t, file, "old\n")

	if err := kshanWeather(c); err != nil {
		t.Fatalf("first failure should only warn: %v", err)
	}
	c.Now = c.Now.Add(5*time.Hour + 30*time.Minute)
	if err := kshanWeather(c); err != nil {
		t.Fatalf("5.5 h should only warn: %v", err)
	}
	c.Now = c.Now.Add(30 * time.Minute)
	if err := kshanWeather(c); err == nil || !strings.Contains(err.Error(), "no weather for 6h0m0s") {
		t.Fatalf("6 h should alert: %v", err)
	}
	c.Now = c.Now.Add(30 * time.Minute)
	if err := kshanWeather(c); err != nil {
		t.Fatalf("alert only once: %v", err)
	}
	if raw, _ := os.ReadFile(file); string(raw) != "old\n" {
		t.Error("old file not kept")
	}
	status = 200
	if err := kshanWeather(c); err != nil || !strings.Contains(out.String(), "Weather working again") {
		t.Fatalf("recovery: %v\n%s", err, out)
	}
	if _, err := os.Stat(failFile(c)); err == nil {
		t.Error("failure mark not cleared")
	}
}

// A dry run prints the diff and writes nothing (no file, no failure mark).
func TestWeatherDryRun(t *testing.T) {
	status := 200
	srv := meteo(t, &status)
	c, dir, _ := testCtx(t, "kshan_weather", map[string]any{"url": srv.URL}, true)
	if err := kshanWeather(c); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "vault", "logs", "kshan", "weather.md")); err == nil {
		t.Error("dry run wrote the file")
	}
}

func TestWMOText(t *testing.T) {
	for code, want := range map[int]string{0: "Clear", 1: "Mostly clear", 2: "Partly cloudy", 3: "Cloudy", 48: "Fog", 55: "Drizzle",
		63: "Rain", 81: "Showers", 73: "Snow", 86: "Snow", 96: "Storm", 42: "—"} {
		if got := wmoText(&code); got != want {
			t.Errorf("%d: %q, want %q", code, got, want)
		}
	}
	if wmoText(nil) != "—" {
		t.Error("nil code")
	}
}
