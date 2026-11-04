package workbench

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestBarFull: on a pane BarWidth wide or wider the bar is four lines and a rule; with colour off the start time
// stands in for the elapsed time and tokens, and a bar with no URL, slices or Asked: line leaves those out.
func TestBarFull(t *testing.T) {
	start := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	now := start.Add(42 * time.Second)
	b := Bar{Repository: "guildhall", Task: "d169", Title: "A title", Step: "build", Slice: 3, Slices: 4, Attempt: 2,
		Asked: "do  the\nthing", URL: "http://h:1/guildhall/d169", Start: start}
	var out bytes.Buffer
	r := NewRenderer(&out, Options{Width: 60, Location: time.UTC})
	r.Event(Usage(Tokens{Output: 900}))
	r.Bar(b, now)
	rule := strings.Repeat("─", 60)
	want := "guildhall · d169 · A title\nbuild · slice 3 of 4 · attempt 2 · started 09:00:00\nAsked: do the thing\nhttp://h:1/guildhall/d169\n" + rule + "\n"
	if out.String() != want {
		t.Errorf("plain bar =\n%q\nwant\n%q", out.String(), want)
	}

	out.Reset()
	r = NewRenderer(&out, Options{Width: 60, Location: time.UTC})
	r.Bar(Bar{Repository: "guildhall", Task: "d169", Step: "grill", Attempt: 1}, now)
	if want := "guildhall · d169\ngrill · attempt 1\n" + rule + "\n"; out.String() != want {
		t.Errorf("bare bar =\n%q\nwant\n%q", out.String(), want)
	}

	out.Reset()
	r = NewRenderer(&out, Options{Width: 80, Color: true, Location: time.UTC})
	r.Event(Usage(Tokens{Input: 1, Output: 900, CacheRead: 400}))
	r.Bar(b, now)
	if !strings.Contains(out.String(), "build · slice 3 of 4 · attempt 2 · started 09:00:00 · 42s · 1.3k tok") {
		t.Errorf("live bar lacks its elapsed time and tokens:\n%s", visible(out.String()))
	}
}

// TestBarOneLine: below BarWidth the bar is one line, fields dropped from the right until it fits, a rule, and the
// URL unbroken however long it is.
func TestBarOneLine(t *testing.T) {
	start := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	b := Bar{Repository: "guildhall", Task: "d169", Title: "ignored", Step: "build", Slice: 3, Slices: 4, Attempt: 1,
		Asked: "ignored", URL: "http://andrew-lap-wsl.tail5da40a.ts.net:4388/guildhall/d169", Start: start}
	for _, tc := range []struct {
		width int
		color bool
		want  string
	}{
		{59, true, "d169 build · attempt 1 · 6m09s · 214k tok"},
		{40, true, "d169 build · attempt 1 · 6m09s"},
		{40, false, "d169 build · attempt 1"},
		{50, false, "d169 build · attempt 1 · started 09:00:00"},
	} {
		var out bytes.Buffer
		r := NewRenderer(&out, Options{Width: tc.width, Color: tc.color, Location: time.UTC})
		r.Event(Usage(Tokens{Input: 214_000}))
		r.Bar(b, start.Add(6*time.Minute+9*time.Second))
		lines := strings.Split(strings.TrimSuffix(stripSGR(out.String()), "\n"), "\n")
		want := []string{tc.want, strings.Repeat("─", tc.width), b.URL}
		if strings.Join(lines, "\n") != strings.Join(want, "\n") {
			t.Errorf("width %d colour %v: %q, want %q", tc.width, tc.color, lines, want)
		}
	}
}

// TestBarCutsAtAWord: a long title or Asked: line is cut at a word, then "…", and control characters are removed.
func TestBarCutsAtAWord(t *testing.T) {
	var out bytes.Buffer
	r := NewRenderer(&out, Options{Width: 60})
	r.Bar(Bar{Repository: "r", Task: "t", Title: "a title long enough that it will not fit on one sixty column line", Step: "s", Attempt: 1,
		Asked: "an asked line \x1b[2Jlong enough that it will not fit on one sixty column line"}, time.Now())
	lines := strings.Split(out.String(), "\n")
	if lines[0] != "r · t · a title long enough that it will not fit on one…" {
		t.Errorf("title line = %q", lines[0])
	}
	if lines[2] != "Asked: an asked line [2Jlong enough that it will not fit…" {
		t.Errorf("Asked: line = %q", lines[2])
	}
}

func stripSGR(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\x1b' {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func TestElapsedAndTokenText(t *testing.T) {
	for d, want := range map[time.Duration]string{0: "0s", 42 * time.Second: "42s", 369 * time.Second: "6m09s", 62 * time.Minute: "1h02m", -time.Second: "0s"} {
		if got := elapsedText(d); got != want {
			t.Errorf("elapsedText(%v) = %q, want %q", d, got, want)
		}
	}
	for n, want := range map[int64]string{512: "512", 1234: "1.2k", 214_999: "214k", 3_400_000: "3.4M"} {
		if got := tokenText(n); got != want {
			t.Errorf("tokenText(%d) = %q, want %q", n, got, want)
		}
	}
}
