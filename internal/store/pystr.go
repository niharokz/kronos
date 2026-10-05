package store

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"gitlab.com/niharokz/kronos/internal/dates"
)

// PyStr renders a YAML value the way Python's str() renders what PyYAML
// loaded, so text built from data (agenda lines, health page values) is
// identical to Taskmaster's output: null → "None", true → "True",
// 10.50 → "10.5", 2026-1-5 → "2026-01-05", lists → "['a', 'b']".
func PyStr(n *yaml.Node) string {
	n = Resolve(n)
	if n == nil {
		return "None"
	}
	switch n.Kind {
	case yaml.SequenceNode:
		parts := make([]string, len(n.Content))
		for i, c := range n.Content {
			parts[i] = PyRepr(c)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case yaml.MappingNode:
		parts := make([]string, 0, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			parts = append(parts, PyRepr(n.Content[i])+": "+PyRepr(n.Content[i+1]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	switch n.ShortTag() {
	case "!!null":
		return "None"
	case "!!bool":
		if strings.EqualFold(n.Value, "true") {
			return "True"
		}
		return "False"
	case "!!int":
		if i, err := ParseInt(n.Value); err == nil {
			return strconv.FormatInt(i, 10)
		}
	case "!!float":
		if f, ok := parseYAMLFloat(n.Value); ok {
			return PyFloat(f)
		}
	case "!!timestamp":
		if d, ok := dates.ParseTimestamp(n.Value); ok {
			rest := strings.TrimSpace(n.Value[strings.IndexAny(n.Value+" ", " Tt"):])
			if rest == "" {
				return dates.Format(d)
			}
			return dates.Format(d) + " " + strings.TrimSuffix(strings.TrimPrefix(rest, "T"), "Z")
		}
	}
	return n.Value
}

// PyRepr is Python's repr() for values nested inside lists/dicts.
func PyRepr(n *yaml.Node) string {
	n = Resolve(n)
	if n != nil && n.Kind == yaml.ScalarNode {
		switch n.ShortTag() {
		case "!!str":
			return pyQuote(n.Value)
		case "!!timestamp":
			if d, ok := dates.ParseTimestamp(n.Value); ok {
				return fmt.Sprintf("datetime.date(%d, %d, %d)", d.Year(), int(d.Month()), d.Day())
			}
		}
	}
	return PyStr(n)
}

func pyQuote(s string) string {
	q := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		q = '"'
	}
	var b strings.Builder
	b.WriteByte(q)
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == rune(q):
			b.WriteByte('\\')
			b.WriteByte(q)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte(q)
	return b.String()
}

func parseYAMLFloat(s string) (float64, bool) {
	switch strings.ToLower(s) {
	case ".inf", "+.inf":
		return math.Inf(1), true
	case "-.inf":
		return math.Inf(-1), true
	case ".nan":
		return math.NaN(), true
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(s, "_", ""), 64)
	return f, err == nil
}

// PyFloat formats a float like Python's repr(): shortest round-trip digits,
// a trailing ".0" for whole numbers, exponent form below 1e-4 or from 1e16.
func PyFloat(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case math.IsNaN(f):
		return "nan"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // e.g. -1.2345e+06
	mant, expStr, _ := strings.Cut(e, "e")
	exp, _ := strconv.Atoi(expStr)
	if exp < -4 || exp >= 16 {
		sign := "+"
		if exp < 0 {
			sign = "-"
			exp = -exp
		}
		return fmt.Sprintf("%se%s%02d", mant, sign, exp)
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// PyHTMLEscape is Python's html.escape(s, quote=True).
func PyHTMLEscape(s string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#x27;",
	).Replace(s)
}
