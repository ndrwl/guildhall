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

// Timestamp is the ISO timestamp GitHub Actions starts each log line with.
var Timestamp = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?Z ?`)

// escape is an ANSI escape sequence, such as the colour codes a step's commands print: a control sequence
// (ESC [ ... final byte), an operating system command (ESC ] ... BEL or ESC \), or a two-byte escape.
var escape = regexp.MustCompile("\x1b(\\[[0-?]*[ -/]*[@-~]|\\][^\x07\x1b]*(\x07|\x1b\\\\)|[@-Z\\\\-_])")

// Excerpt is the log excerpt of a failed job log: the step that holds the log's first ##[error] line, from the
// last ##[group]Run line before it up to the next ##[group]Run line, or the whole log when it has no error; of
// that, the last ExcerptLines lines without their timestamps, trimmed by whole lines from the head to at most
// ExcerptBytes. Before any line is matched, the log's byte order mark, each line's CR and timestamp, and every
// ANSI escape sequence are removed. It depends on the text alone, so the same log always gives the same excerpt.
func Excerpt(text []byte) string {
	lines := normalise(text)
	start, end := 0, len(lines)
	if e, s := firstError(lines); e >= 0 {
		if s >= 0 {
			start = s
		}
		for i := e + 1; i < len(lines); i++ {
			if isRun(lines[i]) {
				end = i
				break
			}
		}
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

// FirstError is the failing step of a job log, read as Excerpt reads it: step is the text of the last
// ##[group]Run line before the log's first ##[error] line, or "" when no such line comes before it, and err is that
// error line's text, each without its marker and surrounding space; ok is false when the log has no ##[error] line.
// It depends on the text alone.
func FirstError(text []byte) (step, err string, ok bool) {
	lines := normalise(text)
	e, s := firstError(lines)
	if e < 0 {
		return "", "", false
	}
	if s >= 0 {
		step = strings.TrimSpace(strings.TrimPrefix(lines[s], runMarker))
	}
	return step, strings.TrimSpace(strings.TrimPrefix(lines[e], errorMarker)), true
}

// The markers GitHub Actions starts a step's group line and an error line with.
const (
	runMarker   = "##[group]Run "
	errorMarker = "##[error]"
)

// normalise splits a job log into lines without the log's byte order mark, each line's CR and timestamp, or any
// ANSI escape sequence.
func normalise(text []byte) []string {
	lines := strings.Split(strings.TrimSuffix(string(text), "\n"), "\n")
	for i, l := range lines {
		l = strings.TrimSuffix(l, "\r")
		if i == 0 {
			l = strings.TrimPrefix(l, bom)
		}
		lines[i] = escape.ReplaceAllString(Timestamp.ReplaceAllString(l, ""), "")
	}
	return lines
}

// isRun reports whether l starts a step's group.
func isRun(l string) bool { return strings.HasPrefix(l, runMarker) }

// firstError returns the index of the first ##[error] line of lines and of the last ##[group]Run line before it,
// each -1 when there is none.
func firstError(lines []string) (e, step int) {
	for e, l := range lines {
		if !strings.HasPrefix(l, errorMarker) {
			continue
		}
		for i := e; i >= 0; i-- {
			if isRun(lines[i]) {
				return e, i
			}
		}
		return e, -1
	}
	return -1, -1
}
