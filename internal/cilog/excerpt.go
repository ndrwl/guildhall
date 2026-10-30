package cilog

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// The bounds of a log excerpt.
const (
	ExcerptLines = 200
	ExcerptBytes = 32 << 10
)

// bom is the byte order mark a GitHub Actions log starts with.
const bom = "\xef\xbb\xbf"

// timestamp is the ISO timestamp GitHub Actions starts each log line with.
var timestamp = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?Z ?`)

// escape is an ANSI escape sequence, such as the colour codes a step's commands print: a control sequence
// (ESC [ ... final byte), an operating system command (ESC ] ... BEL or ESC \), or a two-byte escape.
var escape = regexp.MustCompile("\x1b(\\[[0-?]*[ -/]*[@-~]|\\][^\x07\x1b]*(\x07|\x1b\\\\)|[@-Z\\\\-_])")

// Excerpt is the log excerpt of a failed job log: the step that holds the log's first ##[error] line, from the
// last ##[group]Run line before it up to the next ##[group]Run line, or the whole log when it has no error; of
// that, the last ExcerptLines lines without their timestamps, trimmed by whole lines from the head to at most
// ExcerptBytes. Before any line is matched, the log's byte order mark, each line's CR and timestamp, and every
// ANSI escape sequence are removed. It depends on the text alone, so the same log always gives the same excerpt.
func Excerpt(text []byte) string {
	lines := strings.Split(strings.TrimSuffix(string(text), "\n"), "\n")
	for i, l := range lines {
		l = strings.TrimSuffix(l, "\r")
		if i == 0 {
			l = strings.TrimPrefix(l, bom)
		}
		lines[i] = escape.ReplaceAllString(timestamp.ReplaceAllString(l, ""), "")
	}
	run := func(l string) bool { return strings.HasPrefix(l, "##[group]Run ") }
	start, end := 0, len(lines)
	for e, l := range lines {
		if !strings.HasPrefix(l, "##[error]") {
			continue
		}
		for i := e; i >= 0; i-- {
			if run(lines[i]) {
				start = i
				break
			}
		}
		for i := e + 1; i < len(lines); i++ {
			if run(lines[i]) {
				end = i
				break
			}
		}
		break
	}
	lines = lines[start:end]
	if len(lines) > ExcerptLines {
		lines = lines[len(lines)-ExcerptLines:]
	}
	size := -1
	for _, l := range lines {
		size += len(l) + 1
	}
	for len(lines) > 1 && size > ExcerptBytes {
		size -= len(lines[0]) + 1
		lines = lines[1:]
	}
	out := strings.Join(lines, "\n")
	if len(out) > ExcerptBytes {
		// One line longer than the cap keeps its tail, from a rune boundary.
		i := len(out) - ExcerptBytes
		for i < len(out) && !utf8.RuneStart(out[i]) {
			i++
		}
		out = out[i:]
	}
	return out
}
