package spec

import "strings"

// SummaryHeading is the heading of a spec's Summary block: the short goal, size, slices, changes since the grill and
// open questions that a plan approval ask shows.
const SummaryHeading = "## Summary"

// SummaryMax is the longest Summary block, in bytes, that Summary returns. A longer block counts as none, so the ask
// that shows it falls back to a link to the spec rather than cutting it mid-text.
const SummaryMax = 4 << 10

// Summary reads data's `## Summary` block: from a line that is exactly `## Summary` to the next `#` or `##` heading,
// trimmed, with CRLF read as LF. Fenced code blocks are skipped, so a heading quoted in one is not the block. ok is
// false when the block is missing, empty or longer than SummaryMax bytes. Parsing is a pure function of the bytes.
func Summary(data []byte) (string, bool) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	start, fenced := -1, false
	for i, l := range lines {
		if fence(l) {
			fenced = !fenced
			continue
		}
		if !fenced && strings.TrimRight(l, " \t") == SummaryHeading {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return "", false
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
	s := strings.TrimSpace(strings.Join(lines[start:end], "\n"))
	if s == "" || len(s) > SummaryMax {
		return "", false
	}
	return s, true
}
