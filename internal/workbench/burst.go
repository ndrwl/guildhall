package workbench

import (
	"strconv"
	"strings"
	"time"
)

// BurstSplit is how long a burst of tool calls may stay open before the Workbench writes what it has so far and
// carries on in a new line.
const BurstSplit = 30 * time.Second

// call is one tool call the Renderer has seen, kept by id so its result can name it.
type call struct {
	name, subject string
}

// tally is one kind of tool call counted in a burst: its wording and how often, or on how many distinct paths.
type tally struct {
	key   string
	n     int
	paths map[string]bool
}

// burst is the part of a run of tool calls not yet written: the calls between two pieces of thinking or text, or
// since the last 30 s split.
type burst struct {
	tallies []*tally
	errors  int
	// start is when the part's first call or error came; last is the latest burst event's time.
	start, last time.Time
	// continued is set once a part of this burst has been written, so the next part opens with "…".
	continued bool
}

// empty reports whether the part holds nothing to write.
func (b *burst) empty() bool { return len(b.tallies) == 0 && b.errors == 0 }

// add counts a call of kind key; a non-empty path is counted once however often it comes.
func (b *burst) add(key, path string) {
	var t *tally
	for _, have := range b.tallies {
		if have.key == key {
			t = have
		}
	}
	if t == nil {
		t = &tally{key: key, paths: map[string]bool{}}
		b.tallies = append(b.tallies, t)
	}
	if path != "" {
		if t.paths[path] {
			return
		}
		t.paths[path] = true
	}
	t.n++
}

// callKind is the tally key of a call: the wording it is counted under.
func callKind(name, subject string) (key, path string) {
	switch name {
	case "Read", "NotebookRead":
		return "read", subject
	case "Edit", "MultiEdit", "Write", "NotebookEdit":
		return "edited", subject
	case "Grep", "Glob":
		return "searched", ""
	case "WebFetch":
		return "fetched", ""
	case "Task", "Agent":
		return "started", ""
	case "Bash":
		if p := program(subject); p != "" {
			return "ran " + p, ""
		}
	}
	return name, ""
}

// program is a shell command's program: its first word, or its first two for go, npm and cargo.
func program(command string) string {
	words := strings.Fields(command)
	switch {
	case len(words) == 0:
		return ""
	case len(words) > 1 && (words[0] == "go" || words[0] == "npm" || words[0] == "cargo"):
		return words[0] + " " + words[1]
	}
	return words[0]
}

// phrase is a tally in words: read 1 file, edited 3 files, searched twice, ran go test once, Skill 3 times.
func (t *tally) phrase() string {
	switch t.key {
	case "read", "edited":
		return t.key + " " + count(t.n, "file", "files")
	case "fetched":
		return "fetched " + count(t.n, "page", "pages")
	case "started":
		return "started " + count(t.n, "agent", "agents")
	}
	return t.key + " " + times(t.n)
}

func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// times is once, twice, then "3 times".
func times(n int) string {
	switch n {
	case 1:
		return "once"
	case 2:
		return "twice"
	}
	return strconv.Itoa(n) + " times"
}

// summary is the part in words, as the pieces a wrapped line may break between, joined by spaces. It opens with
// "…" when it continues a written part and ends with "…" when the burst carries on: edited 3 files, ran go test
// once · 1 error.
func (b *burst) summary(open bool) []string {
	var pieces []string
	if b.continued {
		pieces = append(pieces, "…")
	}
	for i, t := range b.tallies {
		p := t.phrase()
		if i < len(b.tallies)-1 {
			p += ","
		}
		pieces = append(pieces, p)
	}
	if b.errors > 0 {
		errs := count(b.errors, "error", "errors")
		if len(b.tallies) > 0 {
			errs = "· " + errs
		}
		pieces = append(pieces, errs)
	}
	if open {
		pieces = append(pieces, "…")
	}
	return pieces
}

// note records a burst event at t, starting the part's 30 s clock with its first call or error, after writing
// the part so far if it has been open that long.
func (r *Renderer) note(t time.Time) {
	r.Tick(t)
	if r.burst.empty() {
		r.burst.start = t
	}
	if t.After(r.burst.last) {
		r.burst.last = t
	}
}

// Tick writes the open burst's calls so far, ending "…", once its part has been open BurstSplit at now; the burst
// carries on in a line starting "…". The line is stamped with the moment the part reached BurstSplit, so a
// replay writes what the live pane wrote.
func (r *Renderer) Tick(now time.Time) {
	b := &r.burst
	if b.empty() || now.Sub(b.start) < BurstSplit {
		return
	}
	at := b.start.Add(BurstSplit)
	r.summaryLine(at, b.summary(true))
	r.burst = burst{continued: true, last: at}
}

// endBurst writes the open burst's summary line, stamped at, or at its last event when at is zero, and closes it.
func (r *Renderer) endBurst(at time.Time) {
	b := &r.burst
	if !at.IsZero() {
		r.Tick(at)
	}
	if !b.empty() {
		if at.IsZero() {
			at = b.last
		}
		r.summaryLine(at, b.summary(false))
	}
	r.burst = burst{}
}

// summaryLine writes a burst's summary, dimmed and stamped at, wrapped between its pieces under its text when it is
// wider than the pane; a piece wider than a whole line is cut.
func (r *Renderer) summaryLine(at time.Time, pieces []string) {
	head := r.clock(at) + " ▸ "
	room := r.opts.Width - len([]rune(head))
	cur := ""
	for _, p := range pieces {
		p = cut(clean(p), room)
		switch {
		case cur == "":
			cur = p
		case len([]rune(cur))+1+len([]rune(p)) <= room:
			cur += " " + p
		default:
			r.line(span{head + cur, dim})
			head, cur = strings.Repeat(" ", len([]rune(head))), p
		}
	}
	r.line(span{head + cur, dim})
}

// failedCall writes the error line of the call id, naming it, in the error colour. A call wider than the pane is
// cut at a word, and what does not fit after it wraps under it.
func (r *Renderer) failedCall(at time.Time, id string, size string) {
	c, ok := r.calls[id]
	if !ok {
		c = call{name: "tool"}
	}
	head := r.clock(at) + " ✗ "
	name := cut(strings.TrimSpace(clean(c.name)+" "+clean(c.subject)), r.opts.Width-len([]rune(head)))
	r.wrapAfter(toRunes(name+" · error, "+size, failure), span{head, failure}, span{strings.Repeat(" ", len([]rune(head))), failure})
}

// cut shortens s to at most n runes, at a word where one fits, then "…".
func cut(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n < 2 {
		return "…"
	}
	short := string(runes[:n-1])
	if i := strings.LastIndexByte(short, ' '); i > 0 {
		short = short[:i]
	}
	return strings.TrimRight(short, " ") + "…"
}
