package workbench

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

// burstLines renders events at width 80 without colour.
func burstLines(events ...Event) string {
	var out bytes.Buffer
	r := NewRenderer(&out, Options{Width: 80, Location: time.UTC})
	for _, e := range events {
		r.Event(e)
	}
	r.Close()
	return out.String()
}

func callAt(id, name, subject string, offset time.Duration) Event {
	return stamped(ToolCall(id, name, subject, nil), offset)
}

// TestBurstWording: calls are counted by kind in the order each kind first came: files read and edited by
// distinct path, searches, pages, agents, Bash by program, and any other tool by name, with once, twice, then N
// times.
func TestBurstWording(t *testing.T) {
	for _, tc := range []struct {
		name  string
		calls [][2]string
		want  string
	}{
		{"one read", [][2]string{{"Read", "a"}}, "read 1 file"},
		{"distinct reads", [][2]string{{"Read", "a"}, {"Read", "a"}, {"Read", "b"}}, "read 2 files"},
		{"edits and writes", [][2]string{{"Edit", "a"}, {"Write", "b"}, {"MultiEdit", "a"}, {"Edit", "c"}}, "edited 3 files"},
		{"searches", [][2]string{{"Grep", ""}, {"Glob", ""}}, "searched twice"},
		{"pages and agents", [][2]string{{"WebFetch", ""}, {"Task", ""}, {"Agent", ""}}, "fetched 1 page, started 2 agents"},
		{"go, npm and cargo take two words", [][2]string{{"Bash", "go test ./..."}, {"Bash", "npm run build"}, {"Bash", "cargo build --release"}, {"Bash", "go test -run X"}},
			"ran go test twice, ran npm run once, ran cargo build once"},
		{"other programs take one", [][2]string{{"Bash", "git status"}, {"Bash", "git diff"}, {"Bash", "git log"}, {"Bash", "go"}}, "ran git 3 times, ran go once"},
		{"an empty command", [][2]string{{"Bash", ""}}, "Bash once"},
		{"other tools by name", [][2]string{{"Skill", ""}, {"TodoWrite", ""}, {"TodoWrite", ""}, {"TodoWrite", ""}, {"TodoWrite", ""}}, "Skill once, TodoWrite 4 times"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events []Event
			for i, c := range tc.calls {
				events = append(events, callAt(fmt.Sprint(i), c[0], c[1], time.Second))
			}
			events = append(events, stamped(Text("done", nil), 2*time.Second))
			if got, want := burstLines(events...), "14:03:11 ▸ "+tc.want+"\ndone\n"; got != want {
				t.Errorf("burst =\n%q\nwant\n%q", got, want)
			}
		})
	}
}

// TestBurstErrors: each error has its own line naming its call when its result comes, and the summary ends with
// the count; a successful result writes nothing.
func TestBurstErrors(t *testing.T) {
	got := burstLines(
		callAt("a", "Bash", "go test ./internal/workbench/...", 0),
		callAt("b", "Read", "x.go", 0),
		callAt("c", "Edit", "x.go", 0),
		stamped(ToolResult("b", false, 10, 1), time.Second),
		stamped(ToolResult("a", true, 1229, 14), 2*time.Second),
		stamped(ToolResult("c", true, 0, 0), 3*time.Second),
		stamped(Thinking("next"), 4*time.Second))
	want := "14:03:11 ✗ Bash go test ./internal/workbench/... · error, 14 lines, 1.2 KB\n" +
		"14:03:12 ✗ Edit x.go · error, empty\n" +
		"14:03:13 ▸ ran go test once, read 1 file, edited 1 file · 2 errors\n" +
		"┃ next\n"
	if got != want {
		t.Errorf("errors =\n%s\nwant\n%s", got, want)
	}
}

// TestBurstSplit: a burst open 30 s from its first call writes what it has so far, ending "…", stamped 30 s after
// that call, whether a later call or the clock finds it due; the rest goes on a line starting "…". A part with
// nothing in it writes nothing.
func TestBurstSplit(t *testing.T) {
	got := burstLines(
		callAt("a", "Read", "a", 0),
		callAt("b", "Read", "b", 29*time.Second),
		callAt("c", "Bash", "make", 31*time.Second),
		callAt("d", "Bash", "make", 50*time.Second),
		callAt("e", "Bash", "make", 70*time.Second),
		stamped(Text("done", nil), 75*time.Second))
	want := "14:03:39 ▸ read 2 files …\n" +
		"14:04:10 ▸ … ran make twice …\n" +
		"14:04:24 ▸ … ran make once\n" +
		"done\n"
	if got != want {
		t.Errorf("split =\n%s\nwant\n%s", got, want)
	}

	var out bytes.Buffer
	r := NewRenderer(&out, Options{Width: 80, Location: time.UTC})
	r.Event(callAt("a", "Bash", "sleep 60", 0))
	r.Tick(at.Add(29 * time.Second))
	if out.Len() != 0 {
		t.Fatalf("a burst open 29 s wrote %q", out.String())
	}
	r.Tick(at.Add(31 * time.Second))
	r.Tick(at.Add(90 * time.Second))
	r.Event(stamped(ToolResult("a", false, 3, 1), 61*time.Second))
	r.Event(stamped(Text("done", nil), 62*time.Second))
	if want := "14:03:39 ▸ ran sleep once …\ndone\n"; out.String() != want {
		t.Errorf("ticked split =\n%q\nwant\n%q", out.String(), want)
	}
}

// TestBurstClosedByOtherOutput: the end, the banner and a plain line close an open burst; an other line does not.
func TestBurstClosedByOtherOutput(t *testing.T) {
	var out bytes.Buffer
	r := NewRenderer(&out, Options{Width: 80, Location: time.UTC})
	r.Event(callAt("a", "Glob", "", 0))
	r.Other("stderr")
	r.Event(stamped(End(1), 2*time.Second))
	r.Event(callAt("b", "Grep", "", 3*time.Second))
	r.Banner(nil)
	want := "stderr\n14:03:11 ▸ searched once\n14:03:11 · exited with status 1\n14:03:12 ▸ searched once\n\n" +
		strings.Repeat("─", 80) + "\n✗ no result written\n"
	if out.String() != want {
		t.Errorf("closed =\n%q\nwant\n%q", out.String(), want)
	}
}

// TestFailedCallIsCut: an error line's call wider than the pane is cut at a word, then "…", and the rest of the
// line wraps under it; a call the renderer never saw is "tool".
func TestFailedCallIsCut(t *testing.T) {
	var out bytes.Buffer
	r := NewRenderer(&out, Options{Width: 40, Location: time.UTC})
	r.Event(callAt("a", "Bash", "go test ./internal/workbench/... -run TestBurst", 0))
	r.Event(stamped(ToolResult("a", true, 100, 2), 0))
	r.Event(stamped(ToolResult("zz", true, 1, 1), 0))
	want := "14:03:09 ✗ Bash go test… · error, 2\n" + "           lines, 100 B\n" + "14:03:09 ✗ tool · error, 1 B\n"
	if got := out.String(); got != want {
		t.Errorf("cut =\n%q\nwant\n%q", out.String(), want)
	}
}

func TestCut(t *testing.T) {
	for _, tc := range []struct {
		s    string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"one two three", 10, "one two…"},
		{"unbroken-word-here", 8, "unbroke…"},
		{"ab", 1, "…"},
	} {
		if got := cut(tc.s, tc.n); got != tc.want {
			t.Errorf("cut(%q, %d) = %q, want %q", tc.s, tc.n, got, tc.want)
		}
	}
}
