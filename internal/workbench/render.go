package workbench

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// style is how one run of text is shown.
type style uint8

const (
	plain style = iota
	bold
	dim
	code
	tool
	failure
	success
)

// sgr is each style's Select Graphic Rendition parameters.
var sgr = [...]string{plain: "0", bold: "1", dim: "2", code: "33", tool: "36", failure: "31", success: "1;32"}

// reset ends every coloured line, and the Workbench, so the pane is left at a normal shell.
const reset = "\x1b[0m"

// Options configure a Renderer.
type Options struct {
	// Width is the pane width text is wrapped to; zero or less is DefaultWidth.
	Width int
	// Color allows SGR codes; without it the output holds no ESC byte.
	Color bool
	// Location is the zone of tool-line timestamps; nil is time.Local.
	Location *time.Location
}

// Renderer writes Workbench lines to a writer. It keeps the name of each tool call by id, so a result is labelled
// with its own call however parallel calls interleave. The first write error is kept and ends all output.
type Renderer struct {
	out   io.Writer
	opts  Options
	calls map[string]string
	fence bool
	err   error
}

// NewRenderer returns a Renderer writing to out.
func NewRenderer(out io.Writer, opts Options) *Renderer {
	if opts.Width <= 0 {
		opts.Width = DefaultWidth
	}
	if opts.Location == nil {
		opts.Location = time.Local
	}
	return &Renderer{out: out, opts: opts, calls: map[string]string{}}
}

// Err is the first error writing to the output.
func (r *Renderer) Err() error { return r.err }

// span is a run of text in one style.
type span struct {
	text string
	st   style
}

// line writes spans as one line: SGR codes only when colour is on, and a reset after any coloured line.
func (r *Renderer) line(spans ...span) {
	var b strings.Builder
	coloured := false
	cur := plain
	for _, s := range spans {
		if r.opts.Color && s.st != cur {
			if s.st != plain || coloured {
				b.WriteString("\x1b[0;" + sgr[s.st] + "m")
				coloured = true
			}
			cur = s.st
		}
		b.WriteString(s.text)
	}
	if coloured {
		b.WriteString(reset)
	}
	b.WriteByte('\n')
	r.write(b.String())
}

func (r *Renderer) write(s string) {
	if r.err != nil {
		return
	}
	_, r.err = io.WriteString(r.out, s)
}

// Println writes s, Guildhall's own text, unchanged and unstyled, then a newline.
func (r *Renderer) Println(s string) { r.write(s + "\n") }

// Close resets the terminal's graphic rendition when colour is on.
func (r *Renderer) Close() {
	if r.opts.Color {
		r.write(reset)
	}
}

// Header writes the attempt's header and a rule under it.
func (r *Renderer) Header(repo, task, step, attempt string) {
	r.line(span{clean("guildhall " + strings.Join([]string{repo, task, step, attempt}, " · ")), bold})
	r.line(span{strings.Repeat("─", r.opts.Width), dim})
}

// Event writes one event.
func (r *Renderer) Event(e Event) {
	switch e.Kind {
	case KindText:
		r.fence = false
		r.Text(e.Text)
		r.fence = false
	case KindToolCall:
		r.calls[e.ID] = e.Name
		r.toolCall(r.clock(e.Time), e.Name, e.Subject)
	case KindToolResult:
		name, ok := r.calls[e.ID]
		if !ok {
			name = "tool"
		}
		size := sizeText(deref(e.Bytes), deref(e.Lines))
		if e.Error != nil && *e.Error {
			r.line(span{r.clock(e.Time) + "   ", dim}, span{"✗ " + clean(name) + " error " + size, failure})
			return
		}
		r.line(span{r.clock(e.Time) + "   ", dim}, span{"✓", success}, span{" " + clean(name) + " " + size, dim})
	case KindEnd:
		r.line(span{r.clock(e.Time) + " · exited with status " + strconv.Itoa(deref(e.Exit)), dim})
	case KindTruncated:
		r.line(span{fmt.Sprintf("· the event file reached its %d MiB cap; the rest of the attempt is shown plain", MaxFileBytes>>20), dim})
	}
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// clock is t's time of day, HH:MM:SS.
func (r *Renderer) clock(t time.Time) string { return t.In(r.opts.Location).Format("15:04:05") }

// toolCall writes a tool call line; an empty clock leaves the timestamp out.
func (r *Renderer) toolCall(clock, name, subject string) {
	text := strings.TrimSpace("▸ " + clean(name) + " " + clean(subject))
	if clock != "" {
		text = clock + " " + text
	}
	r.line(span{text, tool})
}

// Other writes a launcher.log line no event claims: Claude's stderr, guildhall: lines and advance output, dimmed,
// or in the error colour when it reports a denial or an error.
func (r *Renderer) Other(line string) {
	st := dim
	lower := strings.ToLower(line)
	if strings.HasPrefix(line, "guildhall: claude-artisan: Claude was denied") ||
		(strings.HasPrefix(line, "guildhall:") && (strings.Contains(lower, "error") || strings.Contains(lower, "fail"))) {
		st = failure
	}
	r.line(span{clean(line), st})
}

// Plain writes a launcher.log line with no event file to say what it is: a tool: line as a tool call with no
// timestamp, a guildhall: line as another line, and anything else as text.
func (r *Renderer) Plain(line string) {
	switch {
	case strings.HasPrefix(line, "tool: "):
		name, subject, _ := strings.Cut(strings.TrimPrefix(line, "tool: "), " ")
		r.toolCall("", name, subject)
	case strings.HasPrefix(line, "guildhall:"):
		r.Other(line)
	default:
		r.Text(line)
	}
}

// Quiet writes the dimmed quiet line for a pane that has had no new output for d.
func (r *Renderer) Quiet(d time.Duration) { r.line(span{"· quiet for " + quietText(d), dim}) }

// quietText is d in whole minutes, or hours and minutes: 1m, 2m, 1h4m.
func quietText(d time.Duration) string {
	m := int(d / time.Minute)
	switch {
	case m < 60:
		return strconv.Itoa(m) + "m"
	case m%60 == 0:
		return strconv.Itoa(m/60) + "h"
	}
	return fmt.Sprintf("%dh%dm", m/60, m%60)
}

// Result is what the closing banner shows of an attempt's result file.
type Result struct {
	Status  string
	Summary string
}

// Banner writes the closing banner: the result's status and its summary wrapped, or that no result was written.
func (r *Renderer) Banner(res *Result) {
	r.line()
	r.line(span{strings.Repeat("─", r.opts.Width), dim})
	switch {
	case res == nil:
		r.line(span{"✗ no result written", failure})
		return
	case res.Status == "succeeded":
		r.line(span{"✓ succeeded", success})
	default:
		r.line(span{"✗ " + clean(res.Status), failure})
	}
	for _, l := range strings.Split(clean(res.Summary), "\n") {
		r.wrap(toRunes(l, plain), "  ", "  ")
	}
}

// sizeText is a tool result's size: its lines, when it has more than one, and its bytes.
func sizeText(n int64, lines int) string {
	var b string
	switch {
	case n == 0:
		return "empty"
	case n < 1024:
		b = fmt.Sprintf("%d B", n)
	case n < 1<<20:
		b = fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		b = fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	if lines > 1 {
		return fmt.Sprintf("%d lines, %s", lines, b)
	}
	return b
}

// clean strips every control character, so agent text can never carry escape sequences into the pane, and
// expands tabs. It runs before any SGR code is added.
func clean(s string) string {
	s = strings.ReplaceAll(s, "\t", "    ")
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' {
			return -1
		}
		return r
	}, s)
}

// Text writes assistant text as light Markdown: **bold**, `code`, # headings, -, * and 1. lists with a hanging
// indent, and fenced code blocks, dimmed, indented and never wrapped; everything else is wrapped to the width.
// The fence state carries from one call to the next, for text arriving a line at a time.
func (r *Renderer) Text(text string) {
	for _, l := range strings.Split(clean(text), "\n") {
		trimmed := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(trimmed, "```"):
			r.fence = !r.fence
		case r.fence:
			r.line(span{"    " + l, dim})
		case trimmed == "":
			r.line()
		case heading(trimmed) != "":
			r.wrap(inline(heading(trimmed), bold), "", "")
		default:
			if prefix, rest, ok := listItem(l); ok {
				r.wrap(inline(rest, plain), prefix, strings.Repeat(" ", len([]rune(prefix))))
			} else {
				r.wrap(inline(l, plain), "", "")
			}
		}
	}
}

// heading is the text of a # heading line, or empty.
func heading(l string) string {
	n := len(l) - len(strings.TrimLeft(l, "#"))
	if n < 1 || n > 6 || len(l) == n || l[n] != ' ' {
		return ""
	}
	return strings.TrimSpace(l[n:])
}

// listItem splits a list item line into its shown prefix, its indent and bullet, and its text.
func listItem(l string) (prefix, rest string, ok bool) {
	body := strings.TrimLeft(l, " ")
	indent := l[:len(l)-len(body)]
	if strings.HasPrefix(body, "- ") || strings.HasPrefix(body, "* ") {
		return indent + "• ", strings.TrimSpace(body[2:]), true
	}
	digits := len(body) - len(strings.TrimLeft(body, "0123456789"))
	if digits > 0 && digits <= 9 && strings.HasPrefix(body[digits:], ". ") {
		return indent + body[:digits+2], strings.TrimSpace(body[digits+2:]), true
	}
	return "", "", false
}

// styled is one shown rune and its style.
type styled struct {
	r  rune
	st style
}

func toRunes(s string, st style) []styled {
	out := make([]styled, 0, len(s))
	for _, c := range s {
		out = append(out, styled{c, st})
	}
	return out
}

// inline parses **bold** and `code` in s, shown on base; an unclosed marker is shown as written.
func inline(s string, base style) []styled {
	var out []styled
	for len(s) > 0 {
		switch {
		case strings.HasPrefix(s, "`"):
			if end := strings.Index(s[1:], "`"); end > 0 {
				out = append(out, toRunes(s[1:1+end], code)...)
				s = s[2+end:]
				continue
			}
		case strings.HasPrefix(s, "**"):
			if end := strings.Index(s[2:], "**"); end > 0 {
				out = append(out, inline(s[2:2+end], bold)...)
				s = s[4+end:]
				continue
			}
		}
		next := strings.IndexAny(s[1:], "`*")
		if next < 0 {
			next = len(s) - 1
		}
		out = append(out, toRunes(s[:1+next], base)...)
		s = s[1+next:]
	}
	return out
}

// wrap writes runes word-wrapped to the width, the first line after first and the rest after hang.
func (r *Renderer) wrap(runes []styled, first, hang string) {
	var words [][]styled
	var word []styled
	for _, c := range runes {
		if c.r == ' ' {
			if len(word) > 0 {
				words = append(words, word)
				word = nil
			}
			continue
		}
		word = append(word, c)
	}
	if len(word) > 0 {
		words = append(words, word)
	}
	prefix := first
	var cur []styled
	room := func() int { return r.opts.Width - len([]rune(prefix)) }
	flush := func() {
		r.styledLine(prefix, cur)
		prefix, cur = hang, nil
	}
	for _, w := range words {
		for len(w) > room() && room() > 0 {
			// A word longer than a whole line is broken where the line ends.
			if len(cur) > 0 {
				flush()
				continue
			}
			cur = w[:room()]
			w = w[room():]
			flush()
		}
		switch {
		case len(cur) == 0:
			cur = append(cur, w...)
		case len(cur)+1+len(w) <= room():
			cur = append(append(cur, styled{' ', w[0].st}), w...)
		default:
			flush()
			cur = append(cur, w...)
		}
	}
	if len(cur) > 0 || len(words) == 0 {
		flush()
	}
}

// styledLine writes prefix, unstyled, then runes as one line.
func (r *Renderer) styledLine(prefix string, runes []styled) {
	spans := []span{{prefix, plain}}
	for _, c := range runes {
		if last := &spans[len(spans)-1]; last.st == c.st {
			last.text += string(c.r)
		} else {
			spans = append(spans, span{string(c.r), c.st})
		}
	}
	r.line(spans...)
}
