package jobs

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"

	"gitlab.com/niharokz/kronos/internal/dates"
	"gitlab.com/niharokz/kronos/internal/store"
)

// Helpers shared by the event and subscription jobs.

// anchorKey is the field that stores the day of month a month/year rule
// aims for, so a 31st-of-month item doesn't drift after February.
// Only written for days 29–31; see dates.AnchorDay.
const anchorKey = "anchor_day"

// pyGet is Python's str(d.get(key, def)): def when the key is missing,
// otherwise the value as Python would print it (null → "None").
func pyGet(m *yaml.Node, key, def string) string {
	if !store.Has(m, key) {
		return def
	}
	return store.PyStr(store.Get(m, key))
}

// firstTruthy is Python's `a or b or default` over several fields.
func firstTruthy(m *yaml.Node, def string, keys ...string) string {
	for _, k := range keys {
		if v := store.Get(m, k); store.Truthy(v) {
			return store.PyStr(v)
		}
	}
	return def
}

// isOneTime: frequency missing, null, "" or "none" (legacy "once" too —
// Omnimo's shared schema normalises it to "none").
func isOneTime(e *yaml.Node) bool {
	f := store.Get(e, "frequency")
	if store.IsNull(f) {
		return true
	}
	if f.Kind != yaml.ScalarNode || f.ShortTag() != "!!str" {
		return false
	}
	switch f.Value {
	case "", "none", "once":
		return true
	}
	return false
}

// isActive: status is "open" or "pending".
func isActive(e *yaml.Node) bool {
	return store.Equals(e, "status", "open") || store.Equals(e, "status", "pending")
}

// label is how an event is named in log lines: id, else title, else "?".
func label(e *yaml.Node) string { return firstTruthy(e, "?", "id", "title") }

// ruleAnchor returns the anchor day to use when stepping current by rule
// (0 when the rule isn't month/year based, i.e. no anchor).
func ruleAnchor(e *yaml.Node, rule string, current time.Time) int {
	r, ok := dates.ParseRule(rule)
	if !ok || !r.MonthBased() {
		return 0
	}
	stored, _ := store.IntOf(e, anchorKey)
	return dates.AnchorDay(current, stored)
}

// rememberAnchor records the anchor after a date was moved by a month/year
// rule: kept for days 29–31, removed when no longer needed.
func rememberAnchor(e *yaml.Node, rule string, anchor int) {
	r, ok := dates.ParseRule(rule)
	if !ok || !r.MonthBased() {
		return
	}
	if anchor >= 29 {
		store.Set(e, anchorKey, store.Int(anchor))
	} else {
		store.Delete(e, anchorKey)
	}
}

// slugify is Python's "".join(c if c.isalnum() else "-" for c in s.lower()).strip("-").
func slugify(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// readText reads a file like Python's read_text(errors="replace"):
// invalid UTF-8 becomes U+FFFD and line endings become \n.
func readText(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := strings.ToValidUTF8(string(raw), "�")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n"), nil
}

// isDir reports whether path is an existing directory.
func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// isFile reports whether path is a regular file (following symlinks), like
// pathlib's is_file().
func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// missingDir is the error for a required folder that doesn't exist.
func missingDir(path string) error { return fmt.Errorf("Missing directory: %s", path) }
