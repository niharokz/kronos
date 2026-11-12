package kshan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const eventMD = "updated: '2026-11-12 02:55:00'\n\nevents:\n- id: e1\n  title: Old\n  date: '2026-11-01'\n  status: open\n  frequency: none\n  priority: medium\n"

// Kshan drafts become events (time in the title); RahiOS drafts from the old file too; a second
// run, an archived id and an old draft add nothing; the drafts files are never written.
func TestKshanEvents(t *testing.T) {
	c, dir, out := testCtx(t, "kshan_events", nil, false)
	vault := filepath.Join(dir, "vault")
	put(t, filepath.Join(vault, "data", "event.md"), eventMD)
	put(t, filepath.Join(vault, "data", "event.md.bck"), "events:\n- id: kshan-20261101-080000\n  title: Done\n  date: '2026-11-01'\n  status: closed\n")
	kshan := "# Kshan event drafts - Kronos imports these into data/event.md. YAML.\ndrafts:\n" +
		"- text: dentist fri 5pm\n  created: '2026-11-12 19:30:00'\n" +
		"- text: archived already\n  created: '2026-11-01 08:00:00'\n" +
		"- text: too old\n  created: '2026-09-01 10:00:00'\n"
	rahi := "drafts:\n- id: rahi-20261110-101500\n  title: Haircut\n  date: '2026-11-15'\n  time: '11:00'\n  created: '2026-11-10 10:15:00'\n"
	put(t, filepath.Join(vault, "logs", "kshan", "events.md"), kshan)
	put(t, filepath.Join(vault, "logs", "rahi", "events.md"), rahi)

	if err := kshanEvents(c); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	raw, _ := os.ReadFile(filepath.Join(vault, "data", "event.md"))
	s := string(raw)
	for _, want := range []string{
		"  - id: kshan-20261112-193000\n    title: Dentist 17:00\n    date: '2026-11-13'\n    status: open\n    frequency: none\n    priority: medium\n",
		"  - id: rahi-20261110-101500\n    title: Haircut 11:00\n    date: '2026-11-15'\n",
		"  - id: e1\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if strings.Contains(s, "Archived") || strings.Contains(s, "Too old") {
		t.Errorf("archived or old draft imported:\n%s", s)
	}
	if b, _ := os.ReadFile(filepath.Join(vault, "logs", "kshan", "events.md")); string(b) != kshan {
		t.Error("drafts file was written")
	}

	info, _ := os.Stat(filepath.Join(vault, "data", "event.md"))
	if err := kshanEvents(c); err != nil {
		t.Fatal(err)
	}
	again, _ := os.Stat(filepath.Join(vault, "data", "event.md"))
	if !again.ModTime().Equal(info.ModTime()) || !strings.Contains(out.String(), "already imported") {
		t.Error("second run rewrote event.md")
	}
}

// No drafts file → nothing to do; broken drafts or a broken event.md → FAIL, nothing written.
func TestKshanEventsErrors(t *testing.T) {
	c, dir, _ := testCtx(t, "kshan_events", nil, false)
	vault := filepath.Join(dir, "vault")
	if err := kshanEvents(c); err != nil {
		t.Fatalf("no drafts should be fine: %v", err)
	}
	put(t, filepath.Join(vault, "data", "event.md"), eventMD)
	put(t, filepath.Join(vault, "logs", "kshan", "events.md"), "drafts: [unclosed\n")
	if err := kshanEvents(c); err == nil {
		t.Error("broken drafts should fail")
	}
	put(t, filepath.Join(vault, "logs", "kshan", "events.md"), "drafts:\n- text: x tomorrow\n  created: '2026-11-12 10:00:00'\n")
	put(t, filepath.Join(vault, "data", "event.md"), "events: [broken\n")
	if err := kshanEvents(c); err == nil {
		t.Error("broken event.md should fail")
	}
	if b, _ := os.ReadFile(filepath.Join(vault, "data", "event.md")); string(b) != "events: [broken\n" {
		t.Error("broken event.md was overwritten")
	}
}

// paths.kshan_events_old = "" turns the RahiOS fallback off.
func TestKshanEventsNoFallback(t *testing.T) {
	c, dir, _ := testCtx(t, "kshan_events", nil, false)
	c.Cfg.Paths["kshan_events_old"] = ""
	vault := filepath.Join(dir, "vault")
	put(t, filepath.Join(vault, "data", "event.md"), eventMD)
	put(t, filepath.Join(vault, "logs", "rahi", "events.md"), "drafts:\n- id: r1\n  title: X\n  date: '2026-11-15'\n")
	_ = kshanEvents(c)
	if b, _ := os.ReadFile(filepath.Join(vault, "data", "event.md")); strings.Contains(string(b), "r1") {
		t.Error("fallback read although disabled")
	}
}
