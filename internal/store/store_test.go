package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadRecordsErrorsMatchPython(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"missing.md": "Missing file:",
		"bad.md":     "Invalid YAML in bad.md",
		"list.md":    "list.md: root must be a YAML mapping",
		"nokey.md":   "nokey.md: missing 'events' list",
		"scalar.md":  "scalar.md: 'events' must be a list of mappings",
		"mixed.md":   "mixed.md: 'events' must be a list of mappings",
	}
	write(t, dir, "bad.md", "events: [ {bad")
	write(t, dir, "list.md", "- a\n- b\n")
	write(t, dir, "nokey.md", "other: 1\n")
	write(t, dir, "scalar.md", "events: 5\n")
	write(t, dir, "mixed.md", "events:\n- id: a\n- just text\n")
	for name, want := range cases {
		_, err := LoadRecords(filepath.Join(dir, name), "events", true)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", name, err, want)
		}
	}
	// Falsy values are an empty list, like Python's `data[key] or []`.
	for _, body := range []string{"events:\n", "events: []\n", "events: ''\n"} {
		p := write(t, dir, "empty.md", body)
		recs, err := LoadRecords(p, "events", true)
		if err != nil || len(recs) != 0 {
			t.Errorf("%q: %v %v", body, recs, err)
		}
	}
	// Optional + missing → empty, no error.
	if recs, err := LoadRecords(filepath.Join(dir, "nope.md"), "events", false); err != nil || recs != nil {
		t.Errorf("optional missing: %v %v", recs, err)
	}
}

func TestRoundTripKeepsUnknownFieldsAndOrder(t *testing.T) {
	p := write(t, t.TempDir(), "e.md", "events:\n- id: a\n  zeta: 1\n  title: x\n  extra: {nested: [1, 2]}\n")
	recs, err := LoadRecords(p, "events", true)
	if err != nil {
		t.Fatal(err)
	}
	Set(recs[0], "status", Str("open"))
	out, err := RecordsYAML("e.md", "events", recs, time.Date(2026, 10, 5, 2, 30, 0, 0, time.Local))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.HasPrefix(s, "updated: 2026-10-05 02:30:00\n\nevents:\n") {
		t.Errorf("header wrong:\n%s", s)
	}
	order := []string{"id: a", "zeta: 1", "title: x", "extra:", "nested:", "status: open"}
	last := -1
	for _, k := range order {
		i := strings.Index(s, k)
		if i < last {
			t.Fatalf("field order changed, %q out of place:\n%s", k, s)
		}
		last = i
	}
}

func TestRefuseEmptySave(t *testing.T) {
	if _, err := RecordsYAML("event.md", "events", nil, time.Now()); err == nil ||
		err.Error() != "Refusing to save an empty 'events' list to event.md" {
		t.Errorf("err = %v", err)
	}
}

func TestDateOf(t *testing.T) {
	var doc yaml.Node
	yaml.Unmarshal([]byte("a: 2026-1-5\nb: '2026-10-05'\nc: '2026-1-5'\nd: ~\ne: 2026-10-05 10:00:00\n"), &doc)
	m := doc.Content[0]
	for key, want := range map[string]string{"a": "2026-01-05", "b": "2026-10-05", "c": "", "d": "", "e": "2026-10-05"} {
		got, ok := DateOf(m, key)
		if (want == "") == ok || (ok && got.Format("2006-01-02") != want) {
			t.Errorf("DateOf(%s) = %v,%v want %q", key, got, ok, want)
		}
	}
}

func TestPyStr(t *testing.T) {
	var doc yaml.Node
	yaml.Unmarshal([]byte(`
a: true
b: 10.50
c: 10.0
d: ~
e: 2026-1-5
f: [5.1, x, 'y']
g: 007
h: 1e20
i: 0.00001
j: hello
`), &doc)
	m := doc.Content[0]
	for key, want := range map[string]string{
		"a": "True", "b": "10.5", "c": "10.0", "d": "None", "e": "2026-01-05",
		"f": "[5.1, 'x', 'y']", "g": "7", "h": "1e+20", "i": "1e-05", "j": "hello",
	} {
		if got := PyStr(Get(m, key)); got != want {
			t.Errorf("PyStr(%s) = %q want %q", key, got, want)
		}
	}
	if got := PyHTMLEscape(`<a href="x">Mum's & co</a>`); got != "&lt;a href=&quot;x&quot;&gt;Mum&#x27;s &amp; co&lt;/a&gt;" {
		t.Errorf("PyHTMLEscape = %s", got)
	}
}

func TestUniquePath(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "todo.md", "1")
	write(t, dir, "todo_1.md", "2")
	if got := filepath.Base(UniquePath(dir, "todo.md")); got != "todo_2.md" {
		t.Errorf("UniquePath = %s", got)
	}
	if got := filepath.Base(UniquePath(dir, "new.md")); got != "new.md" {
		t.Errorf("UniquePath = %s", got)
	}
}
