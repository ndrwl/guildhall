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

// TestPresentWorkbench: the Workbench shows the status bar from the bridge's fields, the merged stream, the banner
// after the advance output with its elapsed time, cost and tokens and the task's link, and FinishLine unchanged;
// without colour it holds no ESC byte, and with colour it ends reset.
func TestPresentWorkbench(t *testing.T) {
	events := append(bridgeEvents()[:3:3], workbench.Usage(workbench.Tokens{Input: 1000, Output: 1500}), workbench.End(0))
	at := bridgeAttempt(t, bridgeLog, events, adapter.StatusFailed, "Tests fail.")
	clock := func() time.Time { return time.Date(2026, 10, 31, 9, 16, 0, 0, time.UTC) }
	b := Bridge{Attempt: at, CloseAfter: 5 * time.Minute, Title: "Show the bar", Asked: "add a status bar.", URL: "http://h:1/guildhall/d36",
		Start: time.Date(2026, 10, 31, 9, 10, 0, 0, time.UTC)}
	var out bytes.Buffer
	if err := presentWorkbench(b, filepath.Join(at.Dir(), LogFile), exited(), &out, pane{Opts: workbench.Options{Width: 60, Location: time.UTC}, Now: clock}); err != nil {
		t.Fatal(err)
	}
	rule := strings.Repeat("─", 60)
	want := "guildhall · d36 · Show the bar\nbuild · attempt 1 · started 09:10:00\nAsked: add a status bar.\nhttp://h:1/guildhall/d36\n" + rule + "\n" +
		"guildhall: claude-artisan: task grant g adds Bash(go test:*)\n" +
		"I am working.\n" +
		"09:15:00 ▸ ran go test once\n" +
		"09:15:00 · exited with status 0\n" +
		"Done.\nstep done\n" +
		"\n" + rule + "\n✗ failed · 6m00s · 2.5k tokens\n  Tests fail.\nhttp://h:1/guildhall/d36\n" +
		"\n" + FinishLine(5*time.Minute) + "\n"
	if out.String() != want {
		t.Errorf("workbench =\n%s\nwant\n%s", out.String(), want)
	}
	out.Reset()
	if err := presentWorkbench(b, filepath.Join(at.Dir(), LogFile), exited(), &out, pane{Opts: workbench.Options{Width: 60, Height: 24, Color: true, Location: time.UTC}, Now: clock}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out.String(), FinishLine(5*time.Minute)+"\n\x1b[0m") {
		t.Errorf("coloured workbench does not end with FinishLine and a reset: %q", out.String()[max(0, out.Len()-200):])
	}
	// The bar's elapsed time counts from the attempt's start, not the bridge's, so a bridge replaying the files
	// later shows the attempt's time.
	if !strings.Contains(out.String(), "attempt 1 · started 09:10:00 · 6m00s") {
		t.Errorf("coloured bar lacks the elapsed time from the attempt's start:\n%s", out.String())
	}
}

// TestPresentBannerUsesTheUsageFile: the banner's cost and tokens come from the usage file beside the result when
// the runtime wrote one, with a narrow pane's one-line bar and its link first.
func TestPresentBannerUsesTheUsageFile(t *testing.T) {
	at := bridgeAttempt(t, "working\n", nil, adapter.StatusSucceeded, "Done.")
	u := adapter.Usage{Tokens: adapter.Measured, InputTokens: 200_000, OutputTokens: 14_000, Cost: adapter.Estimated, CostUSD: 1.844}
	if err := adapter.WriteUsage(at.Files().ResultFile, u); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 31, 9, 10, 0, 0, time.UTC)
	var out bytes.Buffer
	b := Bridge{Attempt: at, URL: "http://h:1/guildhall/d36", Start: start}
	if err := presentWorkbench(b, filepath.Join(at.Dir(), LogFile), exited(), &out, pane{Opts: workbench.Options{Width: 50, Location: time.UTC},
		Now: func() time.Time { return start.Add(369 * time.Second) }}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "d36 build · attempt 1 · started 09:10:00\n"+strings.Repeat("─", 50)+"\nhttp://h:1/guildhall/d36\nworking\n") {
		t.Errorf("narrow bar =\n%s", out.String())
	}
	if !strings.Contains(out.String(), "✓ succeeded · 6m09s · $1.84 · 214k tokens\n  Done.\nhttp://h:1/guildhall/d36\n") {
		t.Errorf("banner =\n%s", out.String())
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
		if err := presentWorkbench(Bridge{Attempt: at}, filepath.Join(at.Dir(), LogFile), exited(), &out, pane{Now: time.Now}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "working\n") || !strings.Contains(out.String(), "✗ no result written\n\n"+FinishLine(0)) {
			t.Errorf("malformed=%v: %q", malformed, out.String())
		}
	}
}
