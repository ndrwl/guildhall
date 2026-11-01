package spec

import (
	"reflect"
	"strings"
	"testing"
)

const summarised = `# Big work

## Summary

- **Goal:** do it in three.
- **Size:** about 850 lines.
- **Slices:** three, listed below.
- **Changed since the grill:** nothing; the plan follows the grill's agreed change.
- **Open questions:** None.

## Goal

Do it in three.

## Slices

1. **Parser.** The parser; about 300 lines.
2. **Engine.** The engine; about 350 lines.
`

func TestSummary(t *testing.T) {
	want := "- **Goal:** do it in three.\n- **Size:** about 850 lines.\n- **Slices:** three, listed below.\n" +
		"- **Changed since the grill:** nothing; the plan follows the grill's agreed change.\n- **Open questions:** None."
	for _, c := range []struct {
		name, spec, want string
		ok               bool
	}{
		{"present", summarised, want, true},
		{"no block", "# T\n\n## Goal\n\nOne PR.\n", "", false},
		{"empty block", "# T\n\n## Summary\n\n  \n## Goal\n\nx\n", "", false},
		{"crlf", "# T\r\n\r\n## Summary\r\n\r\n- **Goal:** g.\r\n- **Size:** s.\r\n\r\n## Goal\r\n", "- **Goal:** g.\n- **Size:** s.", true},
		{"heading in a fence", "# T\n\n```\n## Summary\n\n- quoted\n```\n", "", false},
		{"fence inside the block", "## Summary\n\n- a\n\n```\n## Goal\n```\n\n- b\n## Goal\n", "- a\n\n```\n## Goal\n```\n\n- b", true},
		{"ends at the next ##", "## Summary\n- a\n### Not the end\n- b\n## Goal\n- c\n", "- a\n### Not the end\n- b", true},
		{"ends at the next #", "## Summary\n- a\n# Next\n- c\n", "- a", true},
		{"at the cap", "## Summary\n" + strings.Repeat("x", SummaryMax) + "\n", strings.Repeat("x", SummaryMax), true},
		{"one byte over the cap", "## Summary\n" + strings.Repeat("x", SummaryMax+1) + "\n", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := Summary([]byte(c.spec))
			if got != c.want || ok != c.ok {
				t.Errorf("Summary = %q, %v; want %q, %v", got, ok, c.want, c.ok)
			}
		})
	}
}

// TestSummaryKeepsSlices: a spec with both a Summary block and a slice list still gives its real slices.
func TestSummaryKeepsSlices(t *testing.T) {
	got, err := Slices([]byte(summarised))
	if err != nil {
		t.Fatal(err)
	}
	want := []Slice{{1, "Parser", "The parser; about 300 lines."}, {2, "Engine", "The engine; about 350 lines."}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Slices = %#v, want %#v", got, want)
	}
}
