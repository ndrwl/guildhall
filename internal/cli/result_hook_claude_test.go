package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/adaptertest"
	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/adapter/claude/claudetest"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/release"
	"github.com/ndrwl/guildhall/internal/store"
)

// lockedBuffer is a bytes.Buffer safe for several goroutines and the test to share, such as the supervisor's, or
// claude.Run's stdout and stderr copiers when both are given the same buffer.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestResultHookCommandIsTheCLIs: the Stop hook the Claude Artisan's settings register is result hook.
func TestResultHookCommandIsTheCLIs(t *testing.T) {
	if want := "guildhall result " + resultHookCommand; claude.ResultHookCommand != want {
		t.Fatalf("claude.ResultHookCommand = %q, want %q", claude.ResultHookCommand, want)
	}
}

// TestClaudeNudged runs the Claude wrapper over the stub Claude Code, with this binary on PATH as guildhall, for a
// session that is about to stop without a draft. The Stop hook its settings register runs guildhall result hook,
// which sends the one nudge and blocks the stop. A session that obeys acknowledges the nudge and writes its result,
// which the wrapper publishes; one that stops again is let through, as stop_hook_active, and the attempt has no
// result, leaving the nudge unacknowledged. A session that wrote its result runs no nudge.
func TestClaudeNudged(t *testing.T) {
	for _, tc := range []struct {
		b       string
		rescued bool
	}{{claudetest.Nudged, true}, {claudetest.NudgedNoResult, false}} {
		t.Run(tc.b, func(t *testing.T) {
			c := newResultCopy(t)
			claudetest.Install(t)
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			if err := os.Symlink(exe, filepath.Join(bin, release.BinaryName(runtime.GOOS))); err != nil {
				t.Fatal(err)
			}
			t.Setenv(roleEnv, "guildhall")
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv(artisan.AttemptEnv, c.key.String())
			t.Chdir(c.work)
			attempt := c.key.Dir(filepath.Join(c.dir, "state"))
			if err := os.MkdirAll(attempt, 0o755); err != nil {
				t.Fatal(err)
			}
			promptFile := filepath.Join(t.TempDir(), "prompt.md")
			writeFile(t, promptFile, claudetest.Prompt(adaptertest.Behaviour(tc.b)))
			a := claude.Args{Identity: adapter.Identity{TaskID: "t1", AttemptID: "plan-1"}, PromptFile: promptFile, ResultFile: filepath.Join(attempt, artisan.ResultFile)}
			var out lockedBuffer
			err = claude.Run(context.Background(), a, &out, &out)
			t.Log(out.String())
			if (err == nil) != tc.rescued {
				t.Fatalf("claude.Run = %v; want rescued %v", err, tc.rescued)
			}
			hooks, err := claudetest.StopHooks(a.ResultFile)
			if want := []string{"stop_hook_active=false block", "stop_hook_active=true pass"}; err != nil || !slices.Equal(hooks, want) {
				t.Errorf("Stop hooks = %q, %v; want %q", hooks, err, want)
			}
			m, ok := c.messagesTo(t)[nudgeID]
			if !ok || m.Sender != store.SenderGuildhall || m.Type != store.MessageNudge || m.Acknowledged != tc.rescued {
				t.Fatalf("nudge = %+v (%v); want one from guildhall, acknowledged %v", m, ok, tc.rescued)
			}
			data, err := os.ReadFile(a.ResultFile)
			if !tc.rescued {
				if !os.IsNotExist(err) {
					t.Errorf("result file: %v; want none", err)
				}
				return
			}
			if r, err := adapter.ParseResult(data, a.Identity); err != nil || r.Status != adapter.StatusSucceeded {
				t.Errorf("published result = %+v, %v; want succeeded", r, err)
			}
		})
	}
}
