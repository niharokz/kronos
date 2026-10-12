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
