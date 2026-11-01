package launch

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/workbench"
)

// bridgeAttempt lays out an attempt whose launcher has written log and whose wrapper has written events, with a
// result file of status and summary unless status is empty.
func bridgeAttempt(t *testing.T, log string, events []workbench.Event, status adapter.Status, summary string) Attempt {
	t.Helper()
	at := Attempt{StateDir: t.TempDir(), Key: artisan.AttemptKey{Repository: "guildhall", TaskID: "d36", Step: "build", Attempt: 1}}
	if err := os.MkdirAll(at.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(at.Dir(), LogFile), []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	if events != nil {
		var buf bytes.Buffer
		w := workbench.NewWriter(&buf)
		w.Now = func() time.Time { return time.Date(2026, 10, 31, 9, 15, 0, 0, time.UTC) }
		for _, e := range events {
			w.Write(e)
		}
		if err := os.WriteFile(filepath.Join(at.Dir(), workbench.File), buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if status != "" {
		r := adapter.Result{Identity: at.Identity(), Status: status, Summary: summary, Outputs: []adapter.Output{}}
		if err := adapter.WriteResult(at.Files().ResultFile, r); err != nil {
			t.Fatal(err)
		}
	}
	return at
}

func exited() <-chan error {
	done := make(chan error, 1)
	done <- nil
	return done
}

const bridgeLog = "guildhall: claude-artisan: task grant g adds Bash(go test:*)\nI am **working**.\ntool: Bash go test ./...\nDone.\nstep done\n"

func bridgeEvents() []workbench.Event {
	return []workbench.Event{
		workbench.Text("I am **working**.", []string{"I am **working**."}),
		workbench.ToolCall("t1", "Bash", "go test ./...", []string{"tool: Bash go test ./..."}),
		workbench.ToolResult("t1", false, 20, 2),
		workbench.End(0),
	}
}

// TestPresentPlainIsTodaysStream: plain output is launcher.log's bytes unchanged, then the banner as plain text
// and FinishLine.
func TestPresentPlainIsTodaysStream(t *testing.T) {
	at := bridgeAttempt(t, bridgeLog, bridgeEvents(), adapter.StatusSucceeded, "Built it.")
	var out bytes.Buffer
	if err := presentPlain(Bridge{Attempt: at, CloseAfter: 0}, filepath.Join(at.Dir(), LogFile), exited(), &out); err != nil {
		t.Fatal(err)
	}
	want := bridgeLog + "\n" + strings.Repeat("─", 80) + "\n✓ succeeded\n  Built it.\n\n" + FinishLine(0) + "\n"
	if out.String() != want {
		t.Errorf("plain =\n%q\nwant\n%q", out.String(), want)
	}
}

// TestPresentWorkbench: the Workbench shows the header, the merged stream, the banner after the advance output,
// and FinishLine unchanged; without colour it holds no ESC byte, and with colour it ends reset.
func TestPresentWorkbench(t *testing.T) {
	at := bridgeAttempt(t, bridgeLog, bridgeEvents(), adapter.StatusFailed, "Tests fail.")
	clock := func() time.Time { return time.Date(2026, 10, 31, 9, 16, 0, 0, time.UTC) }
	var out bytes.Buffer
	if err := presentWorkbench(Bridge{Attempt: at, CloseAfter: 5 * time.Minute}, filepath.Join(at.Dir(), LogFile), exited(), &out,
		workbench.Options{Width: 60, Location: time.UTC}, clock); err != nil {
		t.Fatal(err)
	}
	rule := strings.Repeat("─", 60)
	want := "guildhall guildhall · d36 · build · 1\n" + rule + "\n" +
		"guildhall: claude-artisan: task grant g adds Bash(go test:*)\n" +
		"I am working.\n" +
		"09:15:00 ▸ Bash go test ./...\n" +
		"09:15:00   ✓ Bash 2 lines, 20 B\n" +
		"09:15:00 · exited with status 0\n" +
		"Done.\nstep done\n" +
		"\n" + rule + "\n✗ failed\n  Tests fail.\n" +
		"\n" + FinishLine(5*time.Minute) + "\n"
	if out.String() != want {
		t.Errorf("workbench =\n%s\nwant\n%s", out.String(), want)
	}
	out.Reset()
	if err := presentWorkbench(Bridge{Attempt: at}, filepath.Join(at.Dir(), LogFile), exited(), &out, workbench.Options{Width: 60, Color: true}, clock); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out.String(), FinishLine(0)+"\n\x1b[0m") {
		t.Errorf("coloured workbench does not end with FinishLine and a reset: %q", out.String()[max(0, out.Len()-200):])
	}
}

// TestPresentWorkbenchWithoutResult: a missing or malformed result file shows that no result was written.
func TestPresentWorkbenchWithoutResult(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		at := bridgeAttempt(t, "working\n", nil, "", "")
		if malformed {
			if err := os.WriteFile(at.Files().ResultFile, []byte("{not json"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		var out bytes.Buffer
		if err := presentWorkbench(Bridge{Attempt: at}, filepath.Join(at.Dir(), LogFile), exited(), &out, workbench.Options{}, time.Now); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "working\n") || !strings.Contains(out.String(), "✗ no result written\n\n"+FinishLine(0)) {
			t.Errorf("malformed=%v: %q", malformed, out.String())
		}
	}
}
