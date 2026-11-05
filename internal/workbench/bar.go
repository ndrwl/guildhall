package workbench

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// BarWidth is the narrowest pane the status bar is drawn on in full; a narrower one gets it as one line.
const BarWidth = 60

// Bar is what the status bar names of the attempt a pane shows (docs/artisan.md#workbench).
type Bar struct {
	Repository, Task, Title string
	// Step is the step's id without its slice; Slice and Slices are the slice it is for and the accepted spec's
	// slice count, Slices zero when the task has one slice or it is not known.
	Step          string
	Slice, Slices int
	Attempt       int
	// Asked is one line of what the Artisan was given: the accepted spec's goal, or the task detail's first
	// sentence.
	Asked string
	// URL is the task's page, empty with no [web] table.
	URL string
	// Start is when the attempt started, which elapsed time counts from; zero when it is not known.
	Start time.Time
}

// pinText is the fewest rows of text a pinned bar must leave below it; a shorter terminal gets the bar printed
// once.
const pinText = 3

// pin is the status bar a Renderer shows and, when pinned, where.
type pin struct {
	bar Bar
	// top is the scroll region's first row, the row below the bar, zero when the bar is not pinned.
	top int
	// narrow is set while the bar is one line, so its link has been printed in the text.
	narrow bool
	// drawn is the bar as last drawn, so an unchanged bar is not redrawn.
	drawn string
}

// Escape sequences of the pinned bar. They are the only cursor movement the Workbench sends.
const (
	saveCursor    = "\x1b7"
	restoreCursor = "\x1b8"
	resetRegion   = "\x1b[r"
	clearScreen   = "\x1b[H\x1b[2J"
	clearLine     = "\x1b[2K"
)

// region sets the scroll region to rows top to bottom.
func region(top, bottom int) string { return fmt.Sprintf("\x1b[%d;%dr", top, bottom) }

// moveTo moves the cursor to the start of row.
func moveTo(row int) string { return fmt.Sprintf("\x1b[%d;1H", row) }

// Bar writes the status bar, at now: on a pane BarWidth columns or wider, the task and title, the step, slice,
// attempt, start, elapsed time and tokens so far, what the Artisan was asked and the task's page, then a rule; on a
// narrower one, one line of the task, step (without its slice) and attempt, elapsed time and tokens, fields dropped
// from the right until it fits, a rule, and the page's URL unbroken as the first line of the text below.
//
// With colour on and the terminal's height known, with room for pinText rows below it, the bar is pinned: the
// pane is cleared, the bar drawn in its top rows and a scroll region set below it, where the text scrolls; Redraw
// keeps it current, Resize redraws it at a new size and Unpin resets the region. Otherwise it is printed once, and
// the start time stands in for the elapsed time and tokens, which it would leave stale.
func (r *Renderer) Bar(b Bar, now time.Time) {
	r.pin = &pin{bar: b, narrow: r.opts.Width < BarWidth}
	rows, link := r.barRows(b, now, true)
	if !r.pinnable(len(rows)) {
		rows, link = r.barRows(b, now, false)
	}
	if r.pinnable(len(rows)) {
		draw := r.drawBar(rows)
		top := len(rows) + 1
		r.write(clearScreen + draw + region(top, r.opts.Height) + moveTo(top))
		r.pin.top, r.pin.drawn = top, draw
	} else {
		for _, row := range rows {
			r.line(row...)
		}
	}
	if link != "" {
		r.line(span{link, tool})
	}
}

// pinnable reports whether a bar of n rows can be pinned.
func (r *Renderer) pinnable(n int) bool { return r.opts.Color && r.opts.Height >= n+pinText }

// drawBar is rows drawn over the pane's top rows, each cleared first.
func (r *Renderer) drawBar(rows [][]span) string {
	var b strings.Builder
	for i, row := range rows {
		b.WriteString(moveTo(i+1) + clearLine + r.format(row))
	}
	return b.String()
}

// Redraw redraws a pinned bar at now, between a cursor save and restore, so its elapsed time and tokens stay
// current; an unchanged bar is not redrawn.
func (r *Renderer) Redraw(now time.Time) {
	p := r.pin
	if p == nil || p.top == 0 {
		return
	}
	rows, _ := r.barRows(p.bar, now, true)
	draw := r.drawBar(rows)
	if draw == p.drawn {
		return
	}
	r.write(saveCursor + draw + restoreCursor)
	p.drawn = draw
}

// Resize takes the pane's new width and height, zero when unknown, at now: later text wraps to the new width, and
// text already written is not rewrapped. A pinned bar has its region reset, is redrawn at the new width, or as one
// line below BarWidth, with its link printed in the text, and its region set again; the text carries on where it
// was, or at the region's last row when the bar grew over it. A bar that no longer fits is unpinned, and one
// printed once is pinned when it now fits.
func (r *Renderer) Resize(width, height int, now time.Time) {
	if width > 0 {
		r.opts.Width = max(width, MinWidth)
	}
	r.opts.Height = height
	p := r.pin
	if p == nil || !r.opts.Color {
		return
	}
	rows, link := r.barRows(p.bar, now, true)
	narrow := r.opts.Width < BarWidth
	var b strings.Builder
	b.WriteString(saveCursor)
	if p.top > 0 {
		b.WriteString(resetRegion)
	}
	top := 0
	if r.pinnable(len(rows)) {
		top = len(rows) + 1
		p.drawn = r.drawBar(rows)
		b.WriteString(p.drawn)
		// A bar that shrank leaves its old rows in the text's region; they are cleared.
		for row := top; row < p.top; row++ {
			b.WriteString(moveTo(row) + clearLine)
		}
		b.WriteString(region(top, r.opts.Height))
		if p.top > 0 && top <= p.top {
			b.WriteString(restoreCursor)
		} else {
			b.WriteString(moveTo(r.opts.Height))
		}
	} else {
		if p.top == 0 {
			return
		}
		b.WriteString(restoreCursor)
	}
	p.top = top
	r.write(b.String())
	if top > 0 && narrow && !p.narrow && link != "" {
		r.line(span{link, tool})
	}
	p.narrow = narrow
}

// Unpin resets a pinned bar's scroll region, keeping the cursor where the text was, so the pane is left an
// ordinary terminal. It is written even after an output error, and is safe to call more than once.
func (r *Renderer) Unpin() {
	if r.pin == nil || r.pin.top == 0 {
		return
	}
	r.pin.top = 0
	if r.err != nil {
		_, _ = io.WriteString(r.out, saveCursor+resetRegion+restoreCursor)
		return
	}
	r.write(saveCursor + resetRegion + restoreCursor)
}

// barRows are the bar's lines at now, and on a pane narrower than BarWidth the page's URL printed below it. A live
// bar, one that is pinned, shows the elapsed time and tokens; otherwise the start time stands in for them, which a
// bar printed once would leave stale. The number of lines does not depend on live.
func (r *Renderer) barRows(b Bar, now time.Time, live bool) (rows [][]span, link string) {
	if r.opts.Width < BarWidth {
		fields := []string{clean(b.Task + " " + b.Step), "attempt " + strconv.Itoa(b.Attempt)}
		fields = append(fields, r.times(b, now, live, true)...)
		rows = append(rows, []span{{fit(fields, r.opts.Width), bold}}, []span{{strings.Repeat("─", r.opts.Width), dim}})
		return rows, clean(b.URL)
	}
	rows = append(rows, []span{{cut(clean(strings.Join(nonEmpty(b.Repository, b.Task, b.Title), " · ")), r.opts.Width), bold}})
	fields := []string{clean(b.Step)}
	if b.Slices > 1 {
		fields = append(fields, fmt.Sprintf("slice %d of %d", b.Slice, b.Slices))
	}
	fields = append(fields, "attempt "+strconv.Itoa(b.Attempt))
	if !b.Start.IsZero() {
		fields = append(fields, "started "+r.clock(b.Start))
	}
	fields = append(fields, r.times(b, now, live, false)...)
	rows = append(rows, []span{{fit(fields, r.opts.Width), dim}})
	if asked := strings.Join(strings.Fields(clean(b.Asked)), " "); asked != "" {
		rows = append(rows, []span{{"Asked: ", bold}, {cut(asked, r.opts.Width-len("Asked: ")), plain}})
	}
	if b.URL != "" {
		rows = append(rows, []span{{clean(b.URL), tool}})
	}
	rows = append(rows, []span{{strings.Repeat("─", r.opts.Width), dim}})
	return rows, ""
}

// times are the bar's elapsed time and tokens at now when live; otherwise the start time alone on a one-line bar,
// or nothing on a full one, which names the start already.
func (r *Renderer) times(b Bar, now time.Time, live, oneLine bool) []string {
	if !live {
		if oneLine && !b.Start.IsZero() {
			return []string{"started " + r.clock(b.Start)}
		}
		return nil
	}
	var out []string
	if !b.Start.IsZero() {
		out = append(out, elapsedText(now.Sub(b.Start)))
	}
	if n := r.tokens.Total(); n > 0 {
		out = append(out, tokenText(n)+" tok")
	}
	return out
}

func nonEmpty(s ...string) []string {
	var out []string
	for _, v := range s {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}

// fit joins fields with " · ", dropping fields from the right until the line fits width; a first field too long
// alone is cut.
func fit(fields []string, width int) string {
	for n := len(fields); n > 1; n-- {
		if s := strings.Join(fields[:n], " · "); len([]rune(s)) <= width {
			return s
		}
	}
	if len(fields) == 0 {
		return ""
	}
	return cut(fields[0], width)
}

// elapsedText is d as 42s, 6m09s or 1h02m.
func elapsedText(d time.Duration) string {
	d = max(d, 0)
	s := int(d / time.Second)
	switch {
	case s < 60:
		return strconv.Itoa(s) + "s"
	case s < 3600:
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh%02dm", s/3600, s%3600/60)
}

// tokenText is n tokens shortly: 512, 1.2k, 214k, 3.4M.
func tokenText(n int64) string {
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 10_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	case n < 1_000_000:
		return strconv.FormatInt(n/1000, 10) + "k"
	}
	return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
}
