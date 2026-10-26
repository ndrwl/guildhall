package claude_test

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter/adaptertest"
	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/adapter/claude/claudetest"
)

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
	case <-time.After(3 * time.Second):
		t.Fatal("no readable stream while Claude is running")
	}
}
