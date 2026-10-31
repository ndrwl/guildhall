package spec

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const three = `# Big work

## Goal

Do it in three.

## Slices

1. **Parser and range.** The parser and the workflow range.
   About 300 lines in 6 files.
2. **Engine.** Engine scoping; about 350 lines in 8 files.

3. **Views.** Status and Clerk; about 200 lines in 5 files.

## Out of scope

Nothing.
`

func TestSlicesValid(t *testing.T) {
	for _, c := range []struct {
		name string
		spec string
		want []Slice
	}{
		{"no section", "# T\n\n## Goal\n\nOne PR.\n", nil},
		{"heading in a fence", "# T\n\n```\n## Slices\n\n1. **A** b\n```\n", nil},
		{"one entry", "## Slices\n\n1. **Only** all of it, 100 lines.\n", []Slice{{1, "Only", "all of it, 100 lines."}}},
		{"title with a colon", "## Slices\n\n1. **Only**: all of it.\n", []Slice{{1, "Only", "all of it."}}},
		{"three entries", three, []Slice{
			{1, "Parser and range", "The parser and the workflow range.\n   About 300 lines in 6 files."},
			{2, "Engine", "Engine scoping; about 350 lines in 8 files."},
			{3, "Views", "Status and Clerk; about 200 lines in 5 files."},
		}},
		{"crlf", "## Slices\r\n\r\n1. **A** one\r\n2. **B** two\r\n", []Slice{{1, "A", "one"}, {2, "B", "two"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := Slices([]byte(c.spec))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Slices = %#v, want %#v", got, c.want)
			}
		})
	}
}

func TestSlicesRefused(t *testing.T) {
	for _, c := range []struct {
		name, spec, says string
	}{
		{"empty", "## Slices\n\n## Next\n", "lists no slice"},
		{"gap", "## Slices\n\n1. **A** one\n3. **C** three\n", "entry 3 follows entry 1"},
		{"starts at two", "## Slices\n\n2. **B** two\n", "entry 2 follows entry 0"},
		{"prose first", "## Slices\n\nSome words.\n1. **A** one\n", "line 3 is not a numbered slice entry"},
		{"no title", "## Slices\n\n1. **A** one\n2. B two\n", "slice 2 (line 4) has no bold title"},
		{"unclosed title", "## Slices\n\n1. **A one\n", "slice 1 (line 3) has no bold title"},
		{"blank title", "## Slices\n\n1. **  ** one\n", "slice 1 (line 3) has no bold title"},
		{"empty body", "## Slices\n\n1. **A** one\n2. **B**\n", `slice 2 "B" has an empty body`},
		{"duplicate title", "## Slices\n\n1. **A** one\n2. **a** two\n", `slice 2 "a" has the title of slice 1`},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := Slices([]byte(c.spec))
			if !errors.Is(err, ErrSlices) || !strings.Contains(err.Error(), c.says) {
				t.Errorf("Slices err = %v, want ErrSlices saying %q", err, c.says)
			}
		})
	}
}

func TestSlicesIsPure(t *testing.T) {
	a, err := Slices([]byte(three))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Slices([]byte(three))
	if !reflect.DeepEqual(a, b) {
		t.Error("parsing the same bytes twice differs")
	}
}

func TestCheckRevision(t *testing.T) {
	prev, _ := Slices([]byte(three))
	rewritten := []Slice{prev[0], {2, "Engine, smaller", "less"}, {3, "Views", "same"}, {4, "Docs", "more"}}
	for _, c := range []struct {
		name   string
		next   []Slice
		merged int
		says   string
	}{
		{"nothing merged, anything goes", nil, 0, ""},
		{"rewrite unmerged", rewritten, 1, ""},
		{"drop unmerged", prev[:2], 1, ""},
		{"changed merged title", []Slice{{1, "Parser", prev[0].Body}, prev[1]}, 1, `slice 1 "Parser and range" has merged, and the revised spec changes its entry`},
		{"changed merged body", []Slice{{1, prev[0].Title, "other"}, prev[1]}, 1, "changes its entry"},
		{"dropped merged", []Slice{prev[0]}, 2, `slice 2 "Engine" has merged, and the revised spec drops it`},
		{"no list after a merge", nil, 1, "changes its entry"},
		{"nothing left", prev[:1], 1, "leaves nothing to deliver"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := CheckRevision(prev, c.next, c.merged)
			if c.says == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, ErrSlices) || !strings.Contains(err.Error(), c.says) {
				t.Errorf("CheckRevision err = %v, want ErrSlices saying %q", err, c.says)
			}
		})
	}
}
