package claude_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter/adaptertest"
	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/adapter/claude/claudetest"
	"github.com/ndrwl/guildhall/internal/workbench"
)

// TestRunWritesWorkbenchEvents: the wrapper appends the attempt's Workbench event file beside its result file,
// outside the model directory, ending with Claude's exit status once Claude exits.
func TestRunWritesWorkbenchEvents(t *testing.T) {
	claudetest.Install(t)
	spec := testSpec(t)
	if err := os.WriteFile(spec.PromptFile, []byte(claudetest.Prompt(adaptertest.Succeed)), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := claude.Run(context.Background(), args(spec), &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	path := claude.WorkbenchPath(spec.ResultFile)
	if filepath.Dir(path) != filepath.Dir(spec.ResultFile) || filepath.Base(path) != workbench.File {
		t.Errorf("WorkbenchPath = %s, want %s beside %s", path, workbench.File, spec.ResultFile)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	events := workbench.ReadAll(f)
	if len(events) < 2 || events[0].Kind != workbench.KindText {
		t.Fatalf("events = %+v, want the session's text first", events)
	}
	if last := events[len(events)-1]; last.Kind != workbench.KindEnd || last.Exit == nil || *last.Exit != 0 {
		t.Errorf("last event = %+v, want end with exit 0", last)
	}
	for i, e := range events {
		if e.Seq != int64(i+1) || e.Time.IsZero() {
			t.Errorf("event %d = %+v, want seq %d and a time", i, e, i+1)
		}
		for _, l := range e.Log {
			if !strings.Contains(stdout.String(), l+"\n") {
				t.Errorf("event %d's log line %q is not on stdout %q", i, l, stdout.String())
			}
		}
	}
}

func TestReadableStreamBeforeClaudeExits(t *testing.T) {
	claudetest.Install(t)
	spec := testSpec(t)
	if err := os.WriteFile(spec.PromptFile, []byte(claudetest.Prompt(adaptertest.Hang)), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- claude.Run(ctx, args(spec), writer, io.Discard); _ = writer.Close() }()
	defer func() {
		cancel()
		_ = reader.Close()
		if err := <-done; err == nil {
			t.Error("cancelled Claude should report it stopped without a result")
		}
	}()
	visible := make(chan string, 1)
	go func() {
		b := make([]byte, 4096)
		var text strings.Builder
		for {
			n, err := reader.Read(b)
			text.Write(b[:n])
			if strings.Contains(text.String(), "tool: Bash go test ./...") {
				visible <- text.String()
				return
			}
			if err != nil {
				return
			}
		}
	}()
	select {
	case text := <-visible:
		for _, want := range []string{"I am inspecting the task.", "tool: Read src/main.go", "tool: Bash go test ./..."} {
			if !strings.Contains(text, want) {
				t.Errorf("missing %q: %s", want, text)
			}
		}
	case <-time.After(time.Minute):
		t.Fatal("no readable stream while Claude is running")
	}
}
