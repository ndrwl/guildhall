package steward

import (
	"fmt"
	"strings"
)

// diffContext is how many unchanged lines lineDiff shows around each change.
const diffContext = 2

// lineDiff returns a unified-style line diff of a and b, with line endings normalised for display.
func lineDiff(a, b string) string {
	x, y := diffLines(a), diffLines(b)
	// lcs[i][j] is the longest common subsequence of x[i:] and y[j:].
	lcs := make([][]int, len(x)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	type op struct {
		kind byte
		line string
		i, j int
	}
	var ops []op
	i, j := 0, 0
	for i < len(x) || j < len(y) {
		switch {
		case i < len(x) && j < len(y) && x[i] == y[j]:
			ops = append(ops, op{' ', x[i], i, j})
			i, j = i+1, j+1
		case j == len(y) || i < len(x) && lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, op{'-', x[i], i, j})
			i++
		default:
			ops = append(ops, op{'+', y[j], i, j})
			j++
		}
	}
	show := make([]bool, len(ops))
	for k, o := range ops {
		if o.kind != ' ' {
			for c := max(0, k-diffContext); c <= min(len(ops)-1, k+diffContext); c++ {
				show[c] = true
			}
		}
	}
	var out strings.Builder
	for k, o := range ops {
		if !show[k] {
			continue
		}
		if k == 0 || !show[k-1] {
			fmt.Fprintf(&out, "@@ -%d +%d @@\n", o.i+1, o.j+1)
		}
		out.WriteByte(o.kind)
		out.WriteString(o.line)
		out.WriteByte('\n')
	}
	return out.String()
}

func diffLines(s string) []string {
	var lines []string
	for line := range strings.Lines(s) {
		lines = append(lines, strings.TrimRight(line, "\r\n"))
	}
	return lines
}
