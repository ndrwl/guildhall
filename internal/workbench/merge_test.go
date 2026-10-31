package workbench

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// attemptLog is a wrapper run as an owned launcher records it: the launcher's and the wrapper's guildhall: lines,
// Claude's stderr, the wrapper's text and tool lines for each event, the final result text, and advance output.
type attemptLog struct {
	events bytes.Buffer
	log    bytes.Buffer
	w      *Writer
}

func newAttemptLog() *attemptLog {
	a := &attemptLog{}
	a.w = NewWriter(&a.events)
	a.w.Now = func() time.Time { return at }
	return a
}

// show writes e and then its log lines, in the wrapper's order.
func (a *attemptLog) show(e Event, lines ...string) {
	e.Log = lines
	a.w.Write(e)
	for _, l := range lines {
		a.log.WriteString(l + "\n")
	}
}

func (a *attemptLog) other(l string) { a.log.WriteString(l + "\n") }

func sampleAttempt() *attemptLog {
	a := newAttemptLog()
	a.other("guildhall: claude-artisan: task grant g1 adds Bash(go test:*)")
	a.show(Text("Reading the spec.\n\n**Then** testing.", nil), "Reading the spec.", "", "**Then** testing.")
	a.other("claude: warning: something on stderr")
	a.show(ToolCall("t1", "Read", "spec.md", nil), "tool: Read spec.md")
	a.show(ToolCall("t2", "Bash", "go test ./...", nil), "tool: Bash go test ./...")
	a.w.Write(ToolResult("t2", true, 12, 1))
	a.w.Write(ToolResult("t1", false, 300, 10))
	// Text identical to a line no event claims is still shown once each.
	a.other("Reading the spec.")
	a.show(Text("Done.", nil), "Done.")
	a.w.Write(End(0))
	a.other("Done.")
	a.other("guildhall: advance: step done")
	return a
}

// wantMerged is sampleAttempt rendered without colour at width 80.
const wantMerged = "guildhall: claude-artisan: task grant g1 adds Bash(go test:*)\n" +
	"Reading the spec.\n\nThen testing.\n" +
	"claude: warning: something on stderr\n" +
	"14:03:09 ▸ Read spec.md\n" +
	"14:03:09 ▸ Bash go test ./...\n" +
	"14:03:09   ✗ Bash error 12 B\n" +
	"14:03:09   ✓ Read 10 lines, 300 B\n" +
	"Reading the spec.\n" +
	"Done.\n" +
	"14:03:09 · exited with status 0\n" +
	"Done.\n" +
	"guildhall: advance: step done\n"

func render(f func(*Renderer)) string {
	var out bytes.Buffer
	f(NewRenderer(&out, Options{Width: 80, Location: time.UTC}))
	return out.String()
}

func logLines(a *attemptLog) []string {
	return strings.Split(strings.TrimSuffix(a.log.String(), "\n"), "\n")
}

// TestMergeInterleaved: an interleaved launcher.log and event file, read as the bridge reads them, render each
// event instead of its lines, with nothing shown twice and nothing lost.
func TestMergeInterleaved(t *testing.T) {
	a := sampleAttempt()
	events := ReadAll(&a.events)
	got := render(func(r *Renderer) {
		m := NewMerger(r)
		// One event arrives before each line: never behind the lines, as the wrapper's order guarantees.
		next := 0
		for _, l := range logLines(a) {
			if next < len(events) {
				m.Events(events[next])
				next++
			}
			m.Line(l, true)
		}
		m.Events(events[next:]...)
		m.Flush()
	})
	if got != wantMerged {
		t.Errorf("merged =\n%s\nwant\n%s", got, wantMerged)
	}
}

// TestMergeEventsAhead: an event file read well ahead of launcher.log renders the same.
func TestMergeEventsAhead(t *testing.T) {
	a := sampleAttempt()
	got := render(func(r *Renderer) {
		m := NewMerger(r)
		m.Events(ReadAll(&a.events)...)
		for _, l := range logLines(a) {
			m.Line(l, true)
		}
		m.Flush()
	})
	if got != wantMerged {
		t.Errorf("merged =\n%s\nwant\n%s", got, wantMerged)
	}
}

// TestMergeFlushesEventsWhoseLinesNeverCame: an event whose stdout was lost still renders, at the end.
func TestMergeFlushesEventsWhoseLinesNeverCame(t *testing.T) {
	got := render(func(r *Renderer) {
		m := NewMerger(r)
		m.Events(stamped(Text("lost", []string{"lost"}), 0), stamped(ToolResult("x", false, 1, 1), 0))
		m.Line("other", true)
		m.Flush()
	})
	if want := "other\nlost\n14:03:09   ✓ tool 1 B\n"; got != want {
		t.Errorf("flushed =\n%q\nwant\n%q", got, want)
	}
}

// TestMergeResyncsPastAnEventWhoseLinesNeverCame: when a later event's lines arrive while an earlier event still
// waits for its own, which were lost, the earlier event renders then and later events are neither held as other
// lines nor shown twice.
func TestMergeResyncsPastAnEventWhoseLinesNeverCame(t *testing.T) {
	got := render(func(r *Renderer) {
		m := NewMerger(r)
		m.Events(stamped(Text("lost", []string{"lost"}), 0),
			stamped(ToolResult("x", false, 1, 1), 0),
			stamped(ToolCall("t1", "Read", "a.go", []string{"tool: Read a.go"}), 0),
			stamped(Text("two\nlines", []string{"two", "lines"}), 0))
		m.Line("other", true)
		m.Line("tool: Read a.go", true)
		m.Line("two", true)
		m.Line("lines", true)
		m.Flush()
	})
	if want := "other\nlost\n14:03:09   ✓ tool 1 B\n14:03:09 ▸ Read a.go\ntwo\nlines\n"; got != want {
		t.Errorf("resynced =\n%q\nwant\n%q", got, want)
	}
}

// TestMergeTruncatedShowsTheRestPlain: after a truncated event the rest of launcher.log is rendered plainly.
func TestMergeTruncatedShowsTheRestPlain(t *testing.T) {
	got := render(func(r *Renderer) {
		m := NewMerger(r)
		m.Events(stamped(ToolCall("a", "Read", "x", []string{"tool: Read x"}), 0), stamped(Event{Kind: KindTruncated}, 0))
		m.Line("tool: Read x", true)
		m.Line("tool: Bash ls", true)
		m.Events(stamped(Text("ignored", []string{"ignored"}), 0))
		m.Line("more text", true)
		m.Flush()
	})
	want := "14:03:09 ▸ Read x\n· the event file reached its 64 MiB cap; the rest of the attempt is shown plain\n▸ Bash ls\nmore text\n"
	if got != want {
		t.Errorf("truncated =\n%q\nwant\n%q", got, want)
	}
}

// writeAttempt writes a's launcher.log and event file in a new attempt directory.
func writeAttempt(t *testing.T, a *attemptLog, withEvents bool) (logPath, dir string) {
	t.Helper()
	dir = t.TempDir()
	logPath = filepath.Join(dir, "launcher.log")
	if err := os.WriteFile(logPath, a.log.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if withEvents {
		if err := os.WriteFile(filepath.Join(dir, File), a.events.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return logPath, dir
}

// TestSessionReplaysFromTheStart: a bridge reading an attempt's files from offset 0, as a resumed or recovered one
// does, renders the merged history once and no quiet line, however old the events' timestamps.
func TestSessionReplaysFromTheStart(t *testing.T) {
	a := sampleAttempt()
	logPath, dir := writeAttempt(t, a, true)
	now := at.Add(6 * time.Hour)
	got := render(func(r *Renderer) {
		s := NewSession(r, logPath, dir, now)
		if err := s.Poll(now); err != nil {
			t.Fatal(err)
		}
		if err := s.Poll(now.Add(30 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := s.Finish(now.Add(31 * time.Second)); err != nil {
			t.Fatal(err)
		}
	})
	if got != wantMerged {
		t.Errorf("replayed =\n%s\nwant\n%s", got, wantMerged)
	}
}

// TestSessionWithoutEventFile: with no event file, as for another adapter or an older wrapper, launcher.log is
// rendered alone: tool: lines as tool calls without a timestamp, and the rest as text.
func TestSessionWithoutEventFile(t *testing.T) {
	logPath, dir := writeAttempt(t, sampleAttempt(), false)
	got := render(func(r *Renderer) {
		s := NewSession(r, logPath, dir, at)
		if err := s.Finish(at); err != nil {
			t.Fatal(err)
		}
	})
	want := "guildhall: claude-artisan: task grant g1 adds Bash(go test:*)\n" +
		"Reading the spec.\n\nThen testing.\n" +
		"claude: warning: something on stderr\n" +
		"▸ Read spec.md\n▸ Bash go test ./...\n" +
		"Reading the spec.\nDone.\nDone.\nguildhall: advance: step done\n"
	if got != want {
		t.Errorf("fallback =\n%s\nwant\n%s", got, want)
	}
}

// TestSessionTailsLiveAppends: lines and events appended between polls, even a line split across them, render as
// they complete, and a last line without a newline renders at the finish.
func TestSessionTailsLiveAppends(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "launcher.log")
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logf.Close() }()
	var out bytes.Buffer
	r := NewRenderer(&out, Options{Width: 80, Location: time.UTC})
	s := NewSession(r, logPath, dir, at)
	poll := func() {
		t.Helper()
		if err := s.Poll(at); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = logf.WriteString("guildhall: starting\n")
	poll()
	evf, err := os.OpenFile(filepath.Join(dir, File), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = evf.Close() }()
	w := NewWriter(evf)
	w.Now = func() time.Time { return at }
	w.Write(ToolCall("a", "Bash", "ls", []string{"tool: Bash ls"}))
	_, _ = logf.WriteString("tool: Ba")
	poll()
	if strings.Contains(out.String(), "Bash") {
		t.Fatalf("a partial line rendered: %q", out.String())
	}
	_, _ = logf.WriteString("sh ls\n")
	poll()
	w.Write(ToolResult("a", false, 3, 1))
	poll()
	_, _ = logf.WriteString("no newline")
	if err := s.Finish(at); err != nil {
		t.Fatal(err)
	}
	want := "guildhall: starting\n14:03:09 ▸ Bash ls\n14:03:09   ✓ Bash 3 B\nno newline\n"
	if out.String() != want {
		t.Errorf("live =\n%q\nwant\n%q", out.String(), want)
	}
}

// TestQuietLines: with a fake clock, quiet lines come after 1m, 2m and 4m with no new output, measured from live
// arrival; new output starts the count again.
func TestQuietLines(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "launcher.log")
	if err := os.WriteFile(logPath, []byte("old history\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	r := NewRenderer(&out, Options{Width: 80})
	s := NewSession(r, logPath, dir, at)
	for _, d := range []time.Duration{0, 30 * time.Second, 59 * time.Second, time.Minute, 90 * time.Second, 2 * time.Minute, 3 * time.Minute, 4 * time.Minute, 5 * time.Minute} {
		if err := s.Poll(at.Add(d)); err != nil {
			t.Fatal(err)
		}
	}
	if want := "old history\n· quiet for 1m\n· quiet for 2m\n· quiet for 4m\n"; out.String() != want {
		t.Errorf("quiet =\n%q\nwant\n%q", out.String(), want)
	}
	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("new\n")
	_ = f.Close()
	out.Reset()
	for _, d := range []time.Duration{6 * time.Minute, 6*time.Minute + 59*time.Second, 7 * time.Minute} {
		if err := s.Poll(at.Add(d)); err != nil {
			t.Fatal(err)
		}
	}
	if want := "new\n· quiet for 1m\n"; out.String() != want {
		t.Errorf("after new output =\n%q\nwant\n%q", out.String(), want)
	}
}

// TestSessionReportsOutputErrors: a failing pane ends the presentation with its error.
func TestSessionReportsOutputErrors(t *testing.T) {
	logPath, dir := writeAttempt(t, sampleAttempt(), true)
	s := NewSession(NewRenderer(&failingWriter{}, Options{}), logPath, dir, at)
	if err := s.Poll(at); err == nil {
		t.Error("a failing pane reported no error")
	}
}

// TestWriterBounds: an event over MaxEventBytes is not written, the file stops at its limit with one truncated
// event, and a write error stops the writer without a second torn line.
func TestWriterBounds(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	w.Write(Text(strings.Repeat("x", MaxEventBytes), nil))
	w.Write(Text("small", nil))
	got := ReadAll(&buf)
	if len(got) != 1 || got[0].Text != "small" || got[0].Seq != 2 {
		t.Errorf("events = %+v, want only the small one", got)
	}
	buf.Reset()
	w = NewWriter(&buf)
	w.Limit = 600
	for range 10 {
		w.Write(Text("abcdefghij", nil))
	}
	got = ReadAll(&buf)
	if buf.Len() > 600 || got[len(got)-1].Kind != KindTruncated || len(got) < 2 {
		t.Errorf("capped file of %d bytes = %+v", buf.Len(), got)
	}
	fw := &failingWriter{}
	w = NewWriter(fw)
	w.Write(Text("a", nil))
	w.Write(Text("b", nil))
	if fw.n != 1 {
		t.Errorf("writer wrote %d times after a failure, want 1", fw.n)
	}
	var nilWriter *Writer
	nilWriter.Write(Text("a", nil))
}

// TestDecodeIsLenient: unknown fields are ignored; malformed lines, other schemas and versions are refused.
func TestDecodeIsLenient(t *testing.T) {
	if e, ok := Decode([]byte(`{"schema":"guildhall.workbench-event","version":1,"seq":1,"kind":"text","text":"hi","future":true}`)); !ok || e.Text != "hi" {
		t.Errorf("Decode with an unknown field = %+v, %v", e, ok)
	}
	for _, bad := range []string{`{"schema":"guildhall.workbench-event","version":2,"kind":"text"}`, `{"schema":"other","version":1,"kind":"text"}`, `{"schema":"guildhall.workbench-event","version":1}`, `{not json`} {
		if _, ok := Decode([]byte(bad)); ok {
			t.Errorf("Decode(%s) accepted", bad)
		}
	}
	if got := ReadAll(strings.NewReader("{bad\n" + `{"schema":"guildhall.workbench-event","version":1,"kind":"end","exit":2}` + "\n")); len(got) != 1 || *got[0].Exit != 2 {
		t.Errorf("ReadAll = %+v", got)
	}
}

// TestLineTailSplitsOverlongLogLines: launcher.log lines longer than the bound are passed on in pieces, never
// dropped, and event lines that long are dropped.
func TestLineTailSplitsOverlongLogLines(t *testing.T) {
	split := lineTail{limit: 4, split: true}
	if got := split.feed([]byte("abcdefghij\nxy\n")); len(got) != 4 || string(got[0]) != "abcd" || string(got[2]) != "ij" || string(got[3]) != "xy" {
		t.Errorf("split = %q", got)
	}
	drop := lineTail{limit: 4}
	if got := drop.feed([]byte("abcdefghij\nxy\n")); len(got) != 1 || string(got[0]) != "xy" {
		t.Errorf("drop = %q", got)
	}
	if _, ok := drop.rest(); ok {
		t.Error("an empty tail has a rest")
	}
}
