// Package store reads and writes the vault's YAML-in-.md files.
//
// Records are kept as yaml.Node trees instead of Go structs. A Node keeps
// every field — known or not — in its original order with its original
// value type. Fields Natlas or Omnimo add are carried through untouched,
// and a new field lands at the end of its record, exactly like assigning
// to a Python dict.
//
// Every load/save rule here mirrors Taskmaster's config/common.py
// (load_yaml, load_records, save_records), including its error messages.
package store

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"gitlab.com/niharokz/kronos/internal/dates"
)

// LoadMapping parses a YAML file whose root must be a mapping.
// Missing file: error if required, else an empty mapping. Empty file: an
// empty mapping.
func LoadMapping(path string, required bool) (*yaml.Node, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if required {
			return nil, fmt.Errorf("Missing file: %s", path)
		}
		return NewMapping(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("Cannot read %s: %w", path, err)
	}
	return ParseMapping(raw, filepath.Base(path))
}

// ParseMapping parses raw YAML whose root must be a mapping. name is used
// in error messages.
func ParseMapping(raw []byte, name string) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("Invalid YAML in %s: %v", name, err)
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return NewMapping(), nil
	}
	root := Resolve(doc.Content[0])
	if IsNull(root) {
		return NewMapping(), nil
	}
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: root must be a YAML mapping", name)
	}
	return root, nil
}

// LoadRecords returns the list of mappings under key. Python:
// load_records(). A non-required missing/empty file gives an empty list.
func LoadRecords(path, key string, required bool) ([]*yaml.Node, error) {
	data, err := LoadMapping(path, required)
	if err != nil {
		return nil, err
	}
	if len(data.Content) == 0 && !required {
		return nil, nil
	}
	name := filepath.Base(path)
	if !Has(data, key) {
		return nil, fmt.Errorf("%s: missing '%s' list", name, key)
	}
	return RecordList(Get(data, key), name, key)
}

// RecordList validates a node as a list of mappings (Python: `data[key] or []`
// followed by the isinstance checks). A falsy value counts as an empty list.
func RecordList(v *yaml.Node, name, key string) ([]*yaml.Node, error) {
	v = Resolve(v)
	if !Truthy(v) {
		return nil, nil
	}
	if v.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s: '%s' must be a list of mappings", name, key)
	}
	out := make([]*yaml.Node, 0, len(v.Content))
	for _, item := range v.Content {
		item = Resolve(item)
		if item.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%s: '%s' must be a list of mappings", name, key)
		}
		out = append(out, item)
	}
	return out, nil
}

// RecordsYAML renders "updated: <stamp>\n\n" + {key: records}. It refuses an
// empty list so a parse glitch can never wipe a file. Python: save_records().
func RecordsYAML(name, key string, records []*yaml.Node, now time.Time) ([]byte, error) {
	if len(records) == 0 {
		return nil, fmt.Errorf("Refusing to save an empty '%s' list to %s", key, name)
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: records}
	root := NewMapping()
	Set(root, key, seq)
	body, err := Encode(root)
	if err != nil {
		return nil, err
	}
	return append([]byte("updated: "+now.Format("2006-01-02 15:04:05")+"\n\n"), body...), nil
}

// Encode renders a node as block-style YAML with 2-space indents. Mappings
// and lists are switched to block style (Python's default_flow_style=False);
// scalars keep their quoting.
func Encode(n *yaml.Node) ([]byte, error) {
	blockStyle(n)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(n); err != nil {
		return nil, fmt.Errorf("encoding YAML: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encoding YAML: %w", err)
	}
	return buf.Bytes(), nil
}

func blockStyle(n *yaml.Node) {
	if n == nil {
		return
	}
	if n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode {
		n.Style &^= yaml.FlowStyle
	}
	for _, c := range n.Content {
		blockStyle(c)
	}
}

// ── Node helpers ────────────────────────────────────────────────────────

// NewMapping returns an empty mapping node.
func NewMapping() *yaml.Node { return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"} }

// Resolve follows a YAML alias (*anchor) to its target.
func Resolve(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

// Has reports whether mapping m has key.
func Has(m *yaml.Node, key string) bool { return index(m, key) >= 0 }

// Get returns the value for key in mapping m, or nil. With duplicate keys
// the last one wins, as in Python.
func Get(m *yaml.Node, key string) *yaml.Node {
	if i := index(m, key); i >= 0 {
		return Resolve(m.Content[i+1])
	}
	return nil
}

func index(m *yaml.Node, key string) int {
	m = Resolve(m)
	if m == nil || m.Kind != yaml.MappingNode {
		return -1
	}
	for i := len(m.Content) - 2; i >= 0; i -= 2 {
		if m.Content[i].Value == key {
			return i
		}
	}
	return -1
}

// Set replaces the value for key, or appends key at the end of the mapping.
func Set(m *yaml.Node, key string, v *yaml.Node) {
	if i := index(m, key); i >= 0 {
		m.Content[i+1] = v
		return
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
}

// Delete removes every occurrence of key from mapping m.
func Delete(m *yaml.Node, key string) {
	for i := index(m, key); i >= 0; i = index(m, key) {
		m.Content = append(m.Content[:i], m.Content[i+2:]...)
	}
}

// Str returns a plain string node.
func Str(s string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s} }

// Date returns a single-quoted date string node ('2026-10-06'), the same
// form PyYAML writes.
func Date(d time.Time) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: dates.Format(d), Style: yaml.SingleQuotedStyle}
}

// Quoted returns a single-quoted string node.
func Quoted(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s, Style: yaml.SingleQuotedStyle}
}

// Seq returns a list node.
func Seq(items ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: items}
}

// Int returns an integer node.
func Int(i int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(i)}
}

// IsNull reports a missing value or YAML null (~, null, empty).
func IsNull(n *yaml.Node) bool {
	n = Resolve(n)
	return n == nil || (n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null")
}

// Scalar returns the raw text of a non-null scalar.
func Scalar(n *yaml.Node) (string, bool) {
	n = Resolve(n)
	if n == nil || n.Kind != yaml.ScalarNode || n.ShortTag() == "!!null" {
		return "", false
	}
	return n.Value, true
}

// Field returns the raw scalar text of m[key] ("" when missing, null or
// not a scalar).
func Field(m *yaml.Node, key string) string {
	s, _ := Scalar(Get(m, key))
	return s
}

// Equals reports whether m[key] is the string want (Python: d.get(k) == "want").
// Only string values match, as in Python ("5" the string vs 5 the int).
func Equals(m *yaml.Node, key, want string) bool {
	n := Get(m, key)
	return n != nil && n.Kind == yaml.ScalarNode && n.ShortTag() == "!!str" && n.Value == want
}

// Truthy is Python truthiness for a YAML value.
func Truthy(n *yaml.Node) bool {
	n = Resolve(n)
	if n == nil {
		return false
	}
	switch n.Kind {
	case yaml.SequenceNode, yaml.MappingNode:
		return len(n.Content) > 0
	case yaml.ScalarNode:
		switch n.ShortTag() {
		case "!!null":
			return false
		case "!!bool":
			return strings.EqualFold(n.Value, "true")
		case "!!int":
			i, err := ParseInt(n.Value)
			return err != nil || i != 0
		case "!!float":
			f, err := strconv.ParseFloat(strings.ReplaceAll(n.Value, "_", ""), 64)
			return err != nil || f != 0
		case "!!str":
			return n.Value != ""
		}
		return true
	}
	return true
}

// ParseInt parses a YAML integer (decimal, 0x, 0o, 0b, underscores).
func ParseInt(s string) (int64, error) {
	s = strings.ReplaceAll(s, "_", "")
	if strings.HasPrefix(s, "0o") || strings.HasPrefix(s, "0O") {
		return strconv.ParseInt(s[2:], 8, 64)
	}
	return strconv.ParseInt(s, 0, 64)
}

// DateOf parses m[key] the way Python's parse_date() does: an unquoted YAML
// date/timestamp keeps its date part; any other value must be an ISO date
// string.
func DateOf(m *yaml.Node, key string) (time.Time, bool) {
	n := Get(m, key)
	if n == nil || n.Kind != yaml.ScalarNode || n.ShortTag() == "!!null" {
		return time.Time{}, false
	}
	if n.ShortTag() == "!!timestamp" {
		return dates.ParseTimestamp(n.Value)
	}
	return dates.ParseISO(n.Value)
}

// IntOf returns m[key] as an int when it is an integer scalar.
func IntOf(m *yaml.Node, key string) (int, bool) {
	n := Get(m, key)
	if n == nil || n.Kind != yaml.ScalarNode || n.ShortTag() != "!!int" {
		return 0, false
	}
	i, err := ParseInt(n.Value)
	if err != nil {
		return 0, false
	}
	return int(i), true
}

// Copy returns a deep copy of a node.
func Copy(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	c := *n
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, ch := range n.Content {
		c.Content[i] = Copy(ch)
	}
	return &c
}
