package workbench

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the renderer golden files")

// at is the fixed clock of the renderer tests.
var at = time.Date(2026, 10, 31, 14, 3, 9, 0, time.UTC)

func stamped(e Event, offset time.Duration) Event {
	e.Schema, e.Version, e.Time = Schema, Version, at.Add(offset)
	return e
}

// scenario renders one attempt's worth of every kind of line.
func scenario(r *Renderer) {
	r.Header("guildhall", "d36", "build", "1")
	r.Event(stamped(Text("# Plan\n\nI will **read** the spec, then run `go test ./...` across every package in the repository to see where it stands before changing anything.\n\n"+
		"- read `spec.md` and the notes beside it, which explain the merge rule and the quiet line in detail\n"+
		"* run the tests\n"+
		"  - nested item\n"+
		"10. tenth step of a numbered list that is long enough to wrap onto a second line\n\n"+
		"```go\nfunc main() { fmt.Println(\"a code line that is far too long to fit the pane and is never wrapped by the Workbench\") }\n```\n"+
		"Unclosed **markers and a stray ` stay as written.", nil), 0))
	r.Event(stamped(ToolCall("toolu_1", "Read", "/work/spec.md", nil), time.Second))
	r.Event(stamped(ToolCall("toolu_2", "Bash", "go test ./...", nil), 2*time.Second))
	r.Event(stamped(ToolResult("toolu_2", true, 40, 3), 5*time.Second))
	r.Event(stamped(ToolResult("toolu_1", false, 2300, 61), 6*time.Second))
	r.Event(stamped(ToolResult("toolu_9", false, 0, 0), 7*time.Second))
	r.Event(stamped(Text("Injected \x1b[31mred\x1b[0m and \x1b]0;title\x07 and \u009b2J text, and a verylongwordthatdoesnotfitonanylineatallbecauseitkeepsgoingandgoingandgoingpastthewidth.", nil), 8*time.Second))
	r.Other("guildhall: claude-artisan: Claude was denied 1 tool use: Bash(rm -rf /)")
	r.Other("guildhall: advance: build failed: exit status 1")
	r.Other("guildhall: advance: step done")
	r.Other("some Claude stderr \x1b[2J")
	r.Quiet(2 * time.Minute)
	r.Event(stamped(End(0), 9*time.Second))
	r.Banner(&Result{Status: "succeeded", Summary: "Built the Workbench: a formatted Artisan stream in the pane, with tests for the renderer, the merge and the quiet line."})
	r.Close()
}

// visible shows ESC as \e, so a golden file stays readable.
func visible(s string) string { return strings.ReplaceAll(s, "\x1b", `\e`) }

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(visible(got)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if visible(got) != string(want) {
		t.Errorf("%s:\n%s\nwant\n%s", name, visible(got), want)
	}
}

// TestRendererGolden pins the header, Markdown-lite, wrapping, tool and result lines, other lines, the quiet line
// and the banner at widths 80 and 40, with colour on and off.
func TestRendererGolden(t *testing.T) {
	for _, width := range []int{80, 40} {
		for _, color := range []bool{true, false} {
			name := fmt.Sprintf("render-%d-%s", width, map[bool]string{true: "color", false: "plain"}[color])
			t.Run(name, func(t *testing.T) {
				var out bytes.Buffer
				r := NewRenderer(&out, Options{Width: width, Color: color, Location: time.UTC})
				scenario(r)
				if r.Err() != nil {
					t.Fatal(r.Err())
				}
				got := out.String()
				golden(t, name, got)
				checkRendered(t, got, width, color)
			})
		}
	}
}

// checkRendered checks properties every rendering has, beyond its golden file.
func checkRendered(t *testing.T, got string, width int, color bool) {
	t.Helper()
	if !color && strings.ContainsRune(got, '\x1b') {
		t.Error("output without colour holds an ESC byte")
	}
	// Every ESC is the Workbench's own SGR code: agent text's escapes were stripped before any were added.
	for i := range len(got) {
		if got[i] == '\x1b' && !strings.HasPrefix(got[i:], "\x1b[0") {
			t.Fatalf("foreign escape at %d: %q", i, got[i:min(i+12, len(got))])
		}
	}
	for _, bad := range []string{"\x07", "\u009b"} {
		if strings.Contains(got, bad) {
			t.Errorf("output holds control character %q", bad)
		}
	}
	if color && !strings.HasSuffix(got, reset) {
		t.Error("coloured output does not end with an SGR reset")
	}
	plainText := strings.NewReplacer("\x1b[0m", "", "\x1b[0;0m", "", "\x1b[0;1m", "", "\x1b[0;2m", "", "\x1b[0;33m", "", "\x1b[0;36m", "", "\x1b[0;31m", "", "\x1b[0;1;32m", "").Replace(got)
	for _, want := range []string{
		"guildhall guildhall · d36 · build · 1",
		"14:03:10 ▸ Read /work/spec.md",
		"14:03:11 ▸ Bash go test ./...",
		"14:03:14   ✗ Bash error 3 lines, 40 B",
		"14:03:15   ✓ Read 61 lines, 2.2 KB",
		"14:03:16   ✓ tool empty",
		"· quiet for 2m",
		"✓ succeeded",
		"guildhall: advance: step done",
	} {
		if !strings.Contains(plainText, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	for _, l := range strings.Split(plainText, "\n") {
		if n := len([]rune(l)); n > width && !strings.Contains(l, "never wrapped") && !strings.HasPrefix(l, "guildhall: claude-artisan") && !strings.HasPrefix(l, "guildhall: advance: build") {
			t.Errorf("line of %d runes is wider than %d: %q", n, width, l)
		}
	}
}

// TestRendererErrorColour: a failed result, a denial and an error-like guildhall: line use the error colour; a
// plain other line is dimmed.
func TestRendererErrorColour(t *testing.T) {
	var out bytes.Buffer
	r := NewRenderer(&out, Options{Width: 80, Color: true, Location: time.UTC})
	r.Event(stamped(ToolCall("a", "Bash", "false", nil), 0))
	r.Event(stamped(ToolResult("a", true, 5, 1), 0))
	r.Other("guildhall: claude-artisan: Claude was denied 1 tool use: Bash")
	r.Other("guildhall: launch: error: no pane")
	r.Other("stderr chatter")
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	for i, want := range []string{"\x1b[0;36m", "\x1b[0;31m✗ Bash error 5 B", "\x1b[0;31mguildhall: claude-artisan: Claude was denied", "\x1b[0;31mguildhall: launch: error", "\x1b[0;2mstderr chatter"} {
		if !strings.Contains(lines[i], want) {
			t.Errorf("line %d = %q, want it to hold %q", i, lines[i], visible(want))
		}
	}
}

// TestBanner: the closing banner states success, failure or that no result was written, with the summary wrapped.
func TestBanner(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  *Result
		want []string
	}{
		{"succeeded", &Result{Status: "succeeded", Summary: "All done."}, []string{"✓ succeeded", "  All done."}},
		{"failed", &Result{Status: "failed", Summary: "The tests would not pass on Windows, so the work stops here for a decision."},
			[]string{"✗ failed", "  The tests would not pass on Windows, so", "  the work stops here for a decision."}},
		{"missing", nil, []string{"✗ no result written"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			NewRenderer(&out, Options{Width: 42}).Banner(tc.res)
			lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
			if lines[0] != "" || lines[1] != strings.Repeat("─", 42) {
				t.Errorf("banner does not open with a blank line and a rule: %q", lines)
			}
			if got := lines[2:]; strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("banner = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPlainLines: with no event file a tool: line is a tool call with no timestamp, a guildhall: line another
// line, and the rest text, with fences carried across lines.
func TestPlainLines(t *testing.T) {
	var out bytes.Buffer
	r := NewRenderer(&out, Options{Width: 80})
	for _, l := range []string{"Working on **it**.", "tool: Bash go test ./...", "tool: Glob", "guildhall: advance: step done", "```", "  code", "```", "- item"} {
		r.Plain(l)
	}
	want := "Working on it.\n▸ Bash go test ./...\n▸ Glob\nguildhall: advance: step done\n      code\n• item\n"
	if out.String() != want {
		t.Errorf("plain lines =\n%q\nwant\n%q", out.String(), want)
	}
}

type failingWriter struct{ n int }

func (w *failingWriter) Write(p []byte) (int, error) {
	w.n++
	return 0, fmt.Errorf("pane gone")
}

// TestRendererKeepsTheFirstOutputError: after a failed write the renderer writes nothing more and reports it.
func TestRendererKeepsTheFirstOutputError(t *testing.T) {
	w := &failingWriter{}
	r := NewRenderer(w, Options{})
	r.Other("one")
	r.Other("two")
	if r.Err() == nil || w.n != 1 {
		t.Errorf("Err = %v after %d writes, want the first error after one", r.Err(), w.n)
	}
}

func TestQuietText(t *testing.T) {
	for d, want := range map[time.Duration]string{time.Minute: "1m", 2 * time.Minute: "2m", 64 * time.Minute: "1h4m", 128 * time.Minute: "2h8m", 4 * time.Hour: "4h"} {
		if got := quietText(d); got != want {
			t.Errorf("quietText(%v) = %q, want %q", d, got, want)
		}
	}
}
