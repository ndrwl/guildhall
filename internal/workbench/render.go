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
	thought
	margin
)

// sgr is each style's Select Graphic Rendition parameters. Thinking is italic at full brightness behind a magenta
// margin.
var sgr = [...]string{plain: "0", bold: "1", dim: "2", code: "33", tool: "36", failure: "31", success: "1;32", thought: "3", margin: "35"}

// thinkingMargin opens every line of shown thinking, and marks it alone when colour is off.
const thinkingMargin = "┃ "

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

// Renderer writes Workbench lines to a writer. It keeps each tool call by id, so an error is labelled with its own
// call however parallel calls interleave, and counts the calls of the open burst for its summary line. The first
// write error is kept and ends all output.
type Renderer struct {
	out   io.Writer
	opts  Options
	calls map[string]call
	burst burst
	fence bool
	// thinking is set while the last line written ended shown thinking, so the next shown thinking is kept apart
	// from it by a blank line.
	thinking bool
	// tokens are the latest KindUsage totals.
	tokens Tokens
	err    error
}

// Tokens are the latest token totals a KindUsage event reported, zero before any.
func (r *Renderer) Tokens() Tokens { return r.tokens }

// NewRenderer returns a Renderer writing to out.
func NewRenderer(out io.Writer, opts Options) *Renderer {
	if opts.Width <= 0 {
		opts.Width = DefaultWidth
	}
	if opts.Location == nil {
		opts.Location = time.Local
	}
	return &Renderer{out: out, opts: opts, calls: map[string]call{}}
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
	r.thinking = false
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

// Close writes any open burst's summary and resets the terminal's graphic rendition when colour is on.
func (r *Renderer) Close() {
	r.endBurst(time.Time{})
	if r.opts.Color {
		r.write(reset)
	}
}

// Event writes one event. Tool calls and results write nothing of their own but are counted in the open burst,
// whose one summary line is written when thinking, text, the end or truncation closes it; a failed call writes its
// own error line as its result arrives.
func (r *Renderer) Event(e Event) {
	switch e.Kind {
	case KindText:
		r.endBurst(e.Time)
		r.fence = false
		r.Text(e.Text)
		r.fence = false
	case KindThinking:
		r.endBurst(e.Time)
		r.Thinking(e.Text)
	case KindToolCall:
		r.calls[e.ID] = call{name: e.Name, subject: e.Subject}
		r.note(e.Time)
		r.burst.add(callKind(e.Name, e.Subject))
	case KindToolResult:
		if e.Error == nil || !*e.Error {
			return
		}
		// An error is counted in the open burst; one arriving after thinking or text closed its burst has only its
		// own line.
		r.Tick(e.Time)
		if !r.burst.empty() {
			r.note(e.Time)
			r.burst.errors++
		}
		r.failedCall(e.Time, e.ID, sizeText(deref(e.Bytes), deref(e.Lines)))
	case KindUsage:
		// Usage writes nothing: it keeps the bar's and the banner's tokens current.
		if e.Usage != nil {
			r.tokens = *e.Usage
		}
	case KindEnd:
		r.endBurst(e.Time)
		r.line(span{r.clock(e.Time) + " · exited with status " + strconv.Itoa(deref(e.Exit)), dim})
	case KindTruncated:
		r.endBurst(e.Time)
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

// toolCall writes a plain fallback tool call line, with no timestamp.
func (r *Renderer) toolCall(name, subject string) {
	r.line(span{strings.TrimSpace("▸ " + clean(name) + " " + clean(subject)), tool})
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
	r.endBurst(time.Time{})
	switch {
	case strings.HasPrefix(line, "tool: "):
		name, subject, _ := strings.Cut(strings.TrimPrefix(line, "tool: "), " ")
		r.toolCall(name, subject)
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

// Closing is what the closing banner shows beside the result: how long the attempt took, what it cost and how many
// tokens it used, each left out when it is not known, and the task's page, empty with no [web] table.
type Closing struct {
	Elapsed time.Duration
	CostUSD *float64
	Tokens  int64
	URL     string
}

// Banner writes the closing banner: the result's status, or that no result was written, then c's elapsed time,
// cost and tokens on the same line, the result's summary wrapped, and the task's page as its last line.
func (r *Renderer) Banner(res *Result, c Closing) {
	r.endBurst(time.Time{})
	r.line()
	r.line(span{strings.Repeat("─", r.opts.Width), dim})
	var facts []string
	if c.Elapsed > 0 {
		facts = append(facts, elapsedText(c.Elapsed))
	}
	if c.CostUSD != nil {
		facts = append(facts, fmt.Sprintf("$%.2f", *c.CostUSD))
	}
	if c.Tokens > 0 {
		facts = append(facts, tokenText(c.Tokens)+" tokens")
	}
	// The facts drop from the right until the line fits the pane.
	head := func(s string, st style) {
		for len(facts) > 0 && len([]rune(s+" · "+strings.Join(facts, " · "))) > r.opts.Width {
			facts = facts[:len(facts)-1]
		}
		spans := []span{{s, st}}
		if len(facts) > 0 {
			spans = append(spans, span{" · " + strings.Join(facts, " · "), dim})
		}
		r.line(spans...)
	}
	switch {
	case res == nil:
		head("✗ no result written", failure)
	case res.Status == "succeeded":
		head("✓ succeeded", success)
	default:
		head("✗ "+clean(res.Status), failure)
	}
	if res != nil {
		for _, l := range strings.Split(clean(res.Summary), "\n") {
			r.wrap(toRunes(l, plain), "  ", "  ")
		}
	}
	if c.URL != "" {
		r.line(span{clean(c.URL), tool})
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

// Thinking writes the model's thinking in full, with no Markdown, wrapped to the width behind the magenta margin,
// in italic; a blank line inside it keeps the margin. Shown thinking straight after shown thinking is kept apart by
// a blank line. Empty thinking, which the model did not show, is one dimmed line.
func (r *Renderer) Thinking(text string) {
	text = clean(text)
	if strings.TrimSpace(text) == "" {
		r.line(span{"· thinking not shown by the model", dim})
		return
	}
	if r.thinking {
		r.line()
	}
	for _, l := range strings.Split(strings.Trim(text, "\n"), "\n") {
		if strings.TrimSpace(l) == "" {
			r.line(span{strings.TrimRight(thinkingMargin, " "), margin})
			continue
		}
		r.wrapAfter(toRunes(l, thought), span{thinkingMargin, margin}, span{thinkingMargin, margin})
	}
	r.thinking = true
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

// wrap writes runes word-wrapped to the width, the first line after first and the rest after hang, both unstyled.
func (r *Renderer) wrap(runes []styled, first, hang string) {
	r.wrapAfter(runes, span{first, plain}, span{hang, plain})
}

// wrapAfter is wrap with styled prefixes.
func (r *Renderer) wrapAfter(runes []styled, first, hang span) {
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
	room := func() int { return r.opts.Width - len([]rune(prefix.text)) }
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

// styledLine writes prefix, then runes as one line.
func (r *Renderer) styledLine(prefix span, runes []styled) {
	spans := []span{prefix}
	for _, c := range runes {
		if last := &spans[len(spans)-1]; last.st == c.st {
			last.text += string(c.r)
		} else {
			spans = append(spans, span{string(c.r), c.st})
		}
	}
	r.line(spans...)
}
