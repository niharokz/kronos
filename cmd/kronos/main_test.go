package main

import (
	"path/filepath"
	"testing"

	"gitlab.com/niharokz/kronos/internal/config"
)

// --rahi-path moves rahi, outbox and logs into the sandbox; --now on real data needs --dry-run.
func TestTestOptions(t *testing.T) {
	cfg := &config.Config{Paths: map[string]string{"rahi": "/real/rahi", "outbox": "/real/outbox", "logs": "/real/logs"}}
	sb := t.TempDir()
	o, err := testOptions(cfg, "pulse_watch", false, "2026-11-30 19:00", sb)
	if err != nil || !o.Sandbox || o.Now.Format("2006-01-02 15:04") != "2026-11-30 19:00" {
		t.Fatalf("%+v %v", o, err)
	}
	if cfg.Paths["rahi"] != sb || cfg.Paths["outbox"] != filepath.Join(sb, "_outbox") || cfg.Paths["logs"] != "" {
		t.Errorf("paths %v", cfg.Paths)
	}

	bare := &config.Config{Paths: map[string]string{}}
	for _, tc := range []struct {
		name, now, rahi string
		dry             bool
	}{
		{"pulse_watch", "2026-11-30 19:00", "", false}, // fake clock on real data
		{"pulse_watch", "30/11 19:00", "", true},       // bad layout
		{"calendar_sync", "", sb, true},                // sandbox only for pulse_*
	} {
		if _, err := testOptions(bare, tc.name, tc.dry, tc.now, tc.rahi); err == nil {
			t.Errorf("%+v: want an error", tc)
		}
	}
	if o, err := testOptions(bare, "pulse_watch", true, "2026-11-30 19:00", ""); err != nil || o.Sandbox || o.Now.IsZero() {
		t.Errorf("dry run with --now: %+v %v", o, err)
	}
}

func TestFixtureFlags(t *testing.T) {
	if p, n, err := pkgDays("com.google.android.youtube:3"); err != nil || p != "com.google.android.youtube" || n != 3 {
		t.Errorf("%s %d %v", p, n, err)
	}
	if _, _, err := pkgDays("youtube"); err == nil {
		t.Error("pkg without days accepted")
	}
	if xs, err := ints("15, 16"); err != nil || len(xs) != 2 || xs[1] != 16 {
		t.Errorf("%v %v", xs, err)
	}
	if code := pulseFixture([]string{"--out", filepath.Join(t.TempDir(), "sb"), "--today", "2026-11-30", "--days", "8", "--streak", "x:2"}); code != 0 {
		t.Errorf("exit %d", code)
	}
}
