package job

import (
	"fmt"
	"strings"
)

// Diff renders a compact line diff between a file's current and new
// contents, for `kronos run --dry-run`. Unchanged lines are elided except
// for one line of context around each change.
func Diff(path, old, new string) string {
	if old == new {
		return fmt.Sprintf("= %s unchanged\n", path)
	}
	a := splitLines(old)
	b := splitLines(new)

	// Longest common subsequence table (files here are small).
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}

	type op struct {
		kind byte // ' ', '-', '+'
		text string
	}
	var ops []op
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			ops = append(ops, op{' ', a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, op{'-', a[i]})
			i++
		default:
			ops = append(ops, op{'+', b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		ops = append(ops, op{'-', a[i]})
	}
	for ; j < len(b); j++ {
		ops = append(ops, op{'+', b[j]})
	}

	var out strings.Builder
	if old == "" {
		fmt.Fprintf(&out, "~ would create %s\n", path)
	} else {
		fmt.Fprintf(&out, "~ would change %s\n", path)
	}
	skipped := false
	for k, o := range ops {
		near := o.kind != ' ' ||
			(k > 0 && ops[k-1].kind != ' ') ||
			(k+1 < len(ops) && ops[k+1].kind != ' ')
		if !near {
			skipped = true
			continue
		}
		if skipped {
			out.WriteString("  ...\n")
			skipped = false
		}
		fmt.Fprintf(&out, "%c %s\n", o.kind, o.text)
	}
	return out.String()
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
