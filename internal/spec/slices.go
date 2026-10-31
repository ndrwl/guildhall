// Package spec reads the parts of an accepted spec that Guildhall itself acts on: the ordered slice list of a
// multi-PR task (docs/decisions/multi-pr-tasks.md). Parsing is a pure function of the spec's bytes.
package spec

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// SlicesHeading is the heading of a spec's slice list.
const SlicesHeading = "## Slices"

// ErrSlices reports a spec whose slice list Guildhall refuses: a malformed `## Slices` section, or a revised spec that
// changes or drops a merged slice's entry.
var ErrSlices = errors.New("spec slices refused")

// Slice is one entry of a spec's `## Slices` section: one pull request of a multi-PR task.
type Slice struct {
	// Index is the entry's number, from 1; it is the slice's identity.
	Index int
	// Title is the entry's bold title, display text only.
	Title string
	// Body is the rest of the entry after its title: what the slice delivers and its expected size, with its
	// continuation lines as written.
	Body string
}

// Slices reads data's `## Slices` section: a numbered list 1., 2., ... with no gaps, each item a bold title and then
// what the slice delivers. A spec with no such section has no slice list and returns nil, a one-slice task. A section
// that is empty, has text that is no item, numbers its items out of order, or has an item with no title, an empty body
// or a title another item has is refused with ErrSlices, naming the entry. Fenced code blocks are skipped, so a
// heading quoted in one is not the section.
func Slices(data []byte) ([]Slice, error) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	start, fenced := -1, false
	for i, l := range lines {
		if fence(l) {
			fenced = !fenced
			continue
		}
		if !fenced && strings.TrimRight(l, " \t") == SlicesHeading {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil, nil
	}
	end := start
	for fenced = false; end < len(lines); end++ {
		if fence(lines[end]) {
			fenced = !fenced
		}
		if !fenced && (strings.HasPrefix(lines[end], "# ") || strings.HasPrefix(lines[end], "## ")) {
			break
		}
	}
	type item struct {
		line  int
		lines []string
	}
	var items []item
	for i := start; i < end; i++ {
		l := lines[i]
		if n, rest, ok := numbered(l); ok {
			if n != len(items)+1 {
				return nil, fmt.Errorf("%w: %s line %d: entry %d follows entry %d; number the slices 1., 2., ... with no gaps", ErrSlices, SlicesHeading, i+1, n, len(items))
			}
			items = append(items, item{line: i + 1, lines: []string{rest}})
			continue
		}
		if strings.TrimSpace(l) == "" {
			if len(items) > 0 {
				items[len(items)-1].lines = append(items[len(items)-1].lines, l)
			}
			continue
		}
		if len(items) == 0 {
			return nil, fmt.Errorf("%w: %s line %d is not a numbered slice entry", ErrSlices, SlicesHeading, i+1)
		}
		items[len(items)-1].lines = append(items[len(items)-1].lines, l)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("%w: %s lists no slice", ErrSlices, SlicesHeading)
	}
	out := make([]Slice, 0, len(items))
	titles := map[string]int{}
	for k, it := range items {
		first := it.lines[0]
		rest, ok := strings.CutPrefix(first, "**")
		var title string
		if ok {
			title, rest, ok = strings.Cut(rest, "**")
		}
		// "**Title.** body" and "**Title**: body" both read as title "Title".
		title = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(title), ".:"))
		rest = strings.TrimPrefix(strings.TrimSpace(rest), ":")
		if !ok || title == "" {
			return nil, fmt.Errorf("%w: slice %d (line %d) has no bold title: start it with **<title>**", ErrSlices, k+1, it.line)
		}
		body := strings.TrimSpace(strings.Join(append([]string{rest}, it.lines[1:]...), "\n"))
		if body == "" {
			return nil, fmt.Errorf("%w: slice %d %q has an empty body: say what it delivers and its expected size", ErrSlices, k+1, title)
		}
		key := strings.ToLower(title)
		if j, dup := titles[key]; dup {
			return nil, fmt.Errorf("%w: slice %d %q has the title of slice %d", ErrSlices, k+1, title, j)
		}
		titles[key] = k + 1
		out = append(out, Slice{Index: k + 1, Title: title, Body: body})
	}
	return out, nil
}

// CheckRevision refuses next, a revised spec's slices, unless it keeps the first merged entries of prev, the accepted
// spec's slices, byte-identical and still lists a slice to deliver after them. A spec with no slice list is one slice.
// It returns nil when merged is 0.
func CheckRevision(prev, next []Slice, merged int) error {
	if merged <= 0 {
		return nil
	}
	prev, next = orOne(prev), orOne(next)
	for k := 1; k <= merged; k++ {
		switch {
		case k > len(next):
			return fmt.Errorf("%w: slice %d %q has merged, and the revised spec drops it", ErrSlices, k, title(prev, k))
		case k <= len(prev) && (prev[k-1].Title != next[k-1].Title || prev[k-1].Body != next[k-1].Body):
			return fmt.Errorf("%w: slice %d %q has merged, and the revised spec changes its entry; keep it byte-identical", ErrSlices, k, title(prev, k))
		}
	}
	if len(next) <= merged {
		return fmt.Errorf("%w: %d slice(s) have merged and the revised spec lists %d, so it leaves nothing to deliver", ErrSlices, merged, len(next))
	}
	return nil
}

// orOne is slices, or the one implicit slice of a spec with no slice list.
func orOne(s []Slice) []Slice {
	if len(s) == 0 {
		return []Slice{{Index: 1}}
	}
	return s
}

func title(s []Slice, k int) string {
	if k <= len(s) {
		return s[k-1].Title
	}
	return ""
}

// numbered reads a list item "<n>. <rest>" at the start of a line.
func numbered(l string) (int, string, bool) {
	digits := 0
	for digits < len(l) && l[digits] >= '0' && l[digits] <= '9' {
		digits++
	}
	if digits == 0 || digits+1 >= len(l) || l[digits] != '.' || l[digits+1] != ' ' {
		return 0, "", false
	}
	n, err := strconv.Atoi(l[:digits])
	if err != nil {
		return 0, "", false
	}
	return n, strings.TrimSpace(l[digits+2:]), true
}

func fence(l string) bool {
	t := strings.TrimSpace(l)
	return strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")
}
