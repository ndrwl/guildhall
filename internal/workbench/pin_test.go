package workbench

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

var pinStart = time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)

func pinBar() Bar {
	return Bar{Repository: "guildhall", Task: "d169", Title: "A title", Step: "build", Attempt: 1, Asked: "pin the bar",
		URL: "http://h:1/guildhall/d169", Start: pinStart}
}

// wideRows is the full bar at width 60 with times as its live fields, drawn over rows 1 to 5, each cleared first.
func wideRows(times string) string {
	return "\x1b[1;1H\x1b[2K\x1b[0;1mguildhall · d169 · A title\x1b[0m" +
		"\x1b[2;1H\x1b[2K\x1b[0;2mbuild · attempt 1 · started 09:00:00 · " + times + "\x1b[0m" +
		"\x1b[3;1H\x1b[2K\x1b[0;1mAsked: \x1b[0;0mpin the bar\x1b[0m" +
		"\x1b[4;1H\x1b[2K\x1b[0;36mhttp://h:1/guildhall/d169\x1b[0m" +
		"\x1b[5;1H\x1b[2K\x1b[0;2m" + strings.Repeat("─", 60) + "\x1b[0m"
}

// TestBarPinned: with colour on and the height known, the bar is set up by clearing the pane, drawing it in the
// top rows and setting the scroll region below it with the cursor at its first row; it writes no newline.
func TestBarPinned(t *testing.T) {
	var out bytes.Buffer
	r := NewRenderer(&out, Options{Width: 60, Height: 24, Color: true, Location: time.UTC})
	r.Bar(pinBar(), pinStart.Add(42*time.Second))
	if want := "\x1b[H\x1b[2J" + wideRows("42s") + "\x1b[6;24r\x1b[6;1H"; out.String() != want {
		t.Errorf("pinned bar =\n%q\nwant\n%q", out.String(), want)
	}
}

// TestBarRedraw: Redraw redraws a changed bar between a cursor save and restore, and writes nothing for an
// unchanged one or a bar that is not pinned: with colour off, the height unknown, or too few rows left for text.
// A bar that is not pinned is printed once with its start time in place of the elapsed time and tokens.
func TestBarRedraw(t *testing.T) {
	var out bytes.Buffer
	r := NewRenderer(&out, Options{Width: 60, Height: 24, Color: true, Location: time.UTC})
	r.Bar(pinBar(), pinStart.Add(42*time.Second))
	out.Reset()
	r.Redraw(pinStart.Add(42*time.Second + 500*time.Millisecond))
	if out.Len() != 0 {
		t.Errorf("an unchanged bar was redrawn: %q", out.String())
	}
	r.Event(Usage(Tokens{Output: 1500}))
	r.Redraw(pinStart.Add(43 * time.Second))
	if want := "\x1b7" + wideRows("43s · 1.5k tok") + "\x1b8"; out.String() != want {
		t.Errorf("redraw =\n%q\nwant\n%q", out.String(), want)
	}

	for _, opts := range []Options{{Width: 60, Color: true}, {Width: 60, Height: 24}, {Width: 60, Height: 7, Color: true},
		{Width: 50, Color: true}, {Width: 50, Height: 4, Color: true}} {
		opts.Location = time.UTC
		out.Reset()
		r := NewRenderer(&out, opts)
		r.Event(Usage(Tokens{Output: 1500}))
		r.Bar(pinBar(), pinStart.Add(42*time.Second))
		if strings.Contains(out.String(), "\x1b[H") || !strings.HasSuffix(out.String(), "\n") {
			t.Errorf("%+v: a bar that cannot be pinned was not printed once: %q", opts, out.String())
		}
		if s := out.String(); !strings.Contains(s, "started 09:00:00") || strings.Contains(s, "42s") ||
			strings.Contains(s, " tok") {
			t.Errorf("%+v: a bar printed once does not show the start time alone: %q", opts, s)
		}
		out.Reset()
		r.Redraw(pinStart.Add(time.Minute))
		r.Unpin()
		if out.Len() != 0 {
			t.Errorf("%+v: a bar printed once was redrawn or unpinned: %q", opts, out.String())
		}
	}
}

// TestBarResize: a resize resets the region, redraws the bar at the new width and sets the region again: narrower
// than BarWidth the bar is one line and its link is printed in the text, and the cursor returns to the text; when
// the bar grows back over the text, the cursor goes to the region's last row. A terminal too short for the bar
// unpins it, and one tall enough again pins it. Later text wraps at the new width.
func TestBarResize(t *testing.T) {
	var out bytes.Buffer
	r := NewRenderer(&out, Options{Width: 60, Height: 24, Color: true, Location: time.UTC})
	r.Bar(pinBar(), pinStart)
	now := pinStart.Add(42 * time.Second)

	out.Reset()
	r.Resize(50, 20, now)
	narrow := "\x1b[1;1H\x1b[2K\x1b[0;1md169 build · attempt 1 · 42s\x1b[0m" +
		"\x1b[2;1H\x1b[2K\x1b[0;2m" + strings.Repeat("─", 50) + "\x1b[0m"
	cleared := "\x1b[3;1H\x1b[2K\x1b[4;1H\x1b[2K\x1b[5;1H\x1b[2K"
	if want := "\x1b7\x1b[r" + narrow + cleared + "\x1b[3;20r\x1b8" + "\x1b[0;36mhttp://h:1/guildhall/d169\x1b[0m\n"; out.String() != want {
		t.Errorf("narrowed =\n%q\nwant\n%q", out.String(), want)
	}

	out.Reset()
	r.Resize(60, 24, now)
	if want := "\x1b7\x1b[r" + wideRows("42s") + "\x1b[6;24r\x1b[24;1H"; out.String() != want {
		t.Errorf("widened =\n%q\nwant\n%q", out.String(), want)
	}

	out.Reset()
	r.Resize(60, 7, now)
	if want := "\x1b7\x1b[r\x1b8"; out.String() != want {
		t.Errorf("too short =\n%q\nwant %q", out.String(), want)
	}
	out.Reset()
	r.Resize(60, 6, now)
	r.Redraw(now.Add(time.Second))
	r.Unpin()
	if out.Len() != 0 {
		t.Errorf("an unpinned bar wrote %q", out.String())
	}
	r.Resize(60, 24, now)
	if want := "\x1b7" + wideRows("42s") + "\x1b[6;24r\x1b[24;1H"; out.String() != want {
		t.Errorf("pinned again =\n%q\nwant\n%q", out.String(), want)
	}

	r.Resize(40, 24, now)
	out.Reset()
	r.Text(strings.Repeat("word ", 12))
	for _, l := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		if n := len([]rune(l)); n > 40 {
			t.Errorf("line of %d runes after a resize to 40: %q", n, l)
		}
	}
}

// TestBarUnpin: Unpin resets the region between a cursor save and restore, once; the banner and Close unpin first.
func TestBarUnpin(t *testing.T) {
	var out bytes.Buffer
	pinned := func() *Renderer {
		r := NewRenderer(&out, Options{Width: 60, Height: 24, Color: true, Location: time.UTC})
		r.Bar(pinBar(), pinStart)
		out.Reset()
		return r
	}
	r := pinned()
	r.Unpin()
	r.Unpin()
	if want := "\x1b7\x1b[r\x1b8"; out.String() != want {
		t.Errorf("unpin = %q, want %q", out.String(), want)
	}

	pinned().Banner(&Result{Status: "succeeded"}, Closing{})
	if !strings.HasPrefix(out.String(), "\x1b7\x1b[r\x1b8\n") {
		t.Errorf("banner does not unpin first: %q", out.String())
	}

	pinned().Close()
	if want := "\x1b7\x1b[r\x1b8" + reset; out.String() != want {
		t.Errorf("close = %q, want %q", out.String(), want)
	}
}

// failAfter fails every write after the first n, recording each one it was asked to make.
type failAfter struct {
	n      int
	writes []string
}

func (w *failAfter) Write(p []byte) (int, error) {
	w.writes = append(w.writes, string(p))
	if len(w.writes) > w.n {
		return 0, errors.New("pane gone")
	}
	return len(p), nil
}

// TestBarUnpinAfterOutputError: the region is reset even after an output error ended all other output.
func TestBarUnpinAfterOutputError(t *testing.T) {
	w := &failAfter{n: 1}
	r := NewRenderer(w, Options{Width: 60, Height: 24, Color: true, Location: time.UTC})
	r.Bar(pinBar(), pinStart)
	r.Text("lost")
	r.Text("not tried")
	if r.Err() == nil {
		t.Fatal("no output error")
	}
	r.Unpin()
	if len(w.writes) != 3 || w.writes[2] != "\x1b7\x1b[r\x1b8" {
		t.Errorf("writes = %q, want the bar, the lost text and the reset", w.writes)
	}
}
