package outbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteFollowsOmnimoContract(t *testing.T) {
	dir := t.TempDir()
	path, err := Write(dir, "kronos", Alert, "event_update", "  Exited with code 1\n")
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(path)
	if !strings.HasSuffix(name, "-alert.json") {
		t.Errorf("file name %s", name)
	}
	var m map[string]any
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["version"] != float64(1) || m["kind"] != "alert" || m["source"] != "kronos" ||
		m["title"] != "event_update" || m["text"] != "Exited with code 1" {
		t.Errorf("message = %v", m)
	}
	if _, err := time.Parse(time.RFC3339, m["created"].(string)); err != nil {
		t.Errorf("created: %v", err)
	}
	if tmp, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(tmp) != 0 {
		t.Errorf("left temp files: %v", tmp)
	}
}

func TestWriteRejects(t *testing.T) {
	if _, err := Write(t.TempDir(), "kronos", Notice, "", "   "); err == nil {
		t.Error("empty text accepted")
	}
	if _, err := Write(t.TempDir(), "kronos", "prompt", "", "x"); err == nil {
		t.Error("unknown kind accepted")
	}
}
