package config

import (
	"strings"
	"testing"
)

func TestParseDefaults(t *testing.T) {
	cfg, err := Parse([]byte("jobs:\n  a: {schedule: '@manual'}\n"), "k.yml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Settings.TickSeconds != 10 || cfg.Settings.CatchUpHours != 48 || !cfg.Settings.Alerts {
		t.Fatalf("defaults not applied: %+v", cfg.Settings)
	}
	if cfg.Paths["events"] == "" || len(cfg.Jobs) != 1 || !cfg.Jobs[0].IsEnabled() {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestParseRejectsUnknownKeys(t *testing.T) {
	cases := map[string]string{
		"top":      "jobz: {}\n",
		"settings": "settings: {tick_second: 5}\njobs: {}\n",
		"job":      "jobs:\n  a: {schedule: '@manual', timout: 5}\n",
	}
	for name, raw := range cases {
		if _, err := Parse([]byte(raw), "k.yml"); err == nil || !strings.Contains(err.Error(), "unknown key") {
			t.Errorf("%s: want unknown-key error, got %v", name, err)
		}
	}
}

func TestParseFreeFormSections(t *testing.T) {
	raw := "paths: {my_extra: /x}\njobs:\n  a:\n    schedule: '@manual'\n    options: {anything: 1, nested: {ok: true}}\n"
	cfg, err := Parse([]byte(raw), "k.yml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Paths["my_extra"] != "/x" || cfg.Jobs[0].Int("anything", 0) != 1 {
		t.Fatalf("free-form sections not kept: %+v", cfg)
	}
}

func TestParseErrors(t *testing.T) {
	for _, raw := range []string{
		"jobs:\n  a: {description: no schedule}\n",
		"jobs:\n  a: {schedule: '@manual'}\n  a: {schedule: '@manual'}\n",
		"jobs: [a, b]\n",
	} {
		if _, err := Parse([]byte(raw), "k.yml"); err == nil {
			t.Errorf("want error for %q", raw)
		}
	}
}

func TestParseSettingsKeepDefaults(t *testing.T) {
	cfg, err := Parse([]byte("settings: {alerts: false}\njobs: {}\n"), "k.yml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Settings.Alerts || cfg.Settings.TimeoutSeconds != 900 || cfg.Settings.TickSeconds != 10 {
		t.Fatalf("settings: %+v", cfg.Settings)
	}
	if cfg, err := Parse(nil, "empty.yml"); err != nil || len(cfg.Jobs) != 0 || cfg.Paths["backups"] == "" {
		t.Fatalf("empty file: %+v %v", cfg, err)
	}
}

// ${KRONOS_…} in values comes from the environment; unset → the default or null; other $ text stays.
func TestParseExpandsEnv(t *testing.T) {
	t.Setenv("KRONOS_TEST_LAT", "12.5")
	t.Setenv("KRONOS_TEST_PLACE", "Old Town, Somewhere")
	t.Setenv("KRONOS_TEST_DIR", "/srv/x")
	raw := `paths: {extra: "${KRONOS_TEST_DIR}/sub"}
jobs:
  a:
    schedule: '@manual'
    options:
      lat: ${KRONOS_TEST_LAT}
      place: "${KRONOS_TEST_PLACE}"
      unset: ${KRONOS_TEST_UNSET}
      fallback: ${KRONOS_TEST_UNSET:-mysuru}
      pattern: '^\$\{HOME\} ${NOT_KRONOS}$'
      ${KRONOS_TEST_LAT}: key-not-expanded
`
	cfg, err := Parse([]byte(raw), "k.yml")
	if err != nil {
		t.Fatal(err)
	}
	j := cfg.Jobs[0]
	if v, ok := j.Options["lat"].(float64); !ok || v != 12.5 {
		t.Errorf("lat = %#v", j.Options["lat"])
	}
	if j.String("place", "") != "Old Town, Somewhere" || j.String("fallback", "") != "mysuru" {
		t.Errorf("options = %#v", j.Options)
	}
	if v, ok := j.Options["unset"]; !ok || v != nil || j.String("unset", "def") != "def" {
		t.Errorf("unset = %#v", v)
	}
	if j.String("pattern", "") != `^\$\{HOME\} ${NOT_KRONOS}$` {
		t.Errorf("pattern changed: %q", j.String("pattern", ""))
	}
	if _, ok := j.Options["${KRONOS_TEST_LAT}"]; !ok {
		t.Errorf("keys must not be expanded: %#v", j.Options)
	}
	if cfg.Paths["extra"] != "/srv/x/sub" {
		t.Errorf("paths.extra = %q", cfg.Paths["extra"])
	}
}
