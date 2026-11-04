package workbench

import (
	"fmt"
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

// Bar writes the status bar once, at now: on a pane BarWidth columns or wider, the task and title, the step,
// slice, attempt, start, elapsed time and tokens so far, what the Artisan was asked and the task's page, then a
// rule; on a narrower one, one line of the task, step (without its slice) and attempt, elapsed time and tokens, fields dropped from the
// right until it fits, a rule, and the page's URL unbroken as the first line of the text below. Without colour the
// start time stands in for the elapsed time and tokens, which a bar printed once would leave stale.
func (r *Renderer) Bar(b Bar, now time.Time) {
	live := r.opts.Color
	if r.opts.Width < BarWidth {
		fields := []string{clean(b.Task + " " + b.Step), "attempt " + strconv.Itoa(b.Attempt)}
		fields = append(fields, r.times(b, now, live, true)...)
		r.line(span{fit(fields, r.opts.Width), bold})
		r.line(span{strings.Repeat("─", r.opts.Width), dim})
		if b.URL != "" {
			r.line(span{clean(b.URL), tool})
		}
		return
	}
	r.line(span{cut(clean(strings.Join(nonEmpty(b.Repository, b.Task, b.Title), " · ")), r.opts.Width), bold})
	fields := []string{clean(b.Step)}
	if b.Slices > 1 {
		fields = append(fields, fmt.Sprintf("slice %d of %d", b.Slice, b.Slices))
	}
	fields = append(fields, "attempt "+strconv.Itoa(b.Attempt))
	if !b.Start.IsZero() {
		fields = append(fields, "started "+r.clock(b.Start))
	}
	fields = append(fields, r.times(b, now, live, false)...)
	r.line(span{fit(fields, r.opts.Width), dim})
	if asked := strings.Join(strings.Fields(clean(b.Asked)), " "); asked != "" {
		r.line(span{"Asked: ", bold}, span{cut(asked, r.opts.Width-len("Asked: ")), plain})
	}
	if b.URL != "" {
		r.line(span{clean(b.URL), tool})
	}
	r.line(span{strings.Repeat("─", r.opts.Width), dim})
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
