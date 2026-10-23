package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
)

// Reload is an existing same-supervisor loop, not another wrapper invocation.
// These assertions retrofit its tab contract; they are not a claimed regression.
func TestDirectorTabReloadPreservesOnePane(t *testing.T) {
	for _, now := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "now"}[now], func(t *testing.T) {
			original := tabCopy(t, "guildhall director ready")
			dir, err := os.MkdirTemp("", "gt")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			if err := os.CopyFS(dir, os.DirFS(original)); err != nil {
				t.Fatal(err)
			}
			input, typing, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = input.Close(); _ = typing.Close() }()
			host := newDirectorTabs()
			host.onRun = func(tab adapter.TabRef, _ string) {
				app, _, _ := supervisorApp(input, time.Millisecond)
				if code := app.Run(supervisorArgv(t, dir, tab)[1:]); code != 0 {
					t.Errorf("supervisor exit %d", code)
				}
			}
			code, out, stderr := runWrapper(t, host, "--tab", "--copy", dir)
			if code != 0 {
				t.Fatalf("start: %d %s %s", code, out, stderr)
			}
			first := lastSession(t, out)
			t.Cleanup(func() { _, _ = typing.WriteString("/exit\n"); host.runs.Wait() })
			waitSessions(t, dir, 1)
			copyRun(t, dir, "director", "handoff", "--note-file", noteFile(t, "Continue in the same pane.\n"))
			args := []string{"director", "reload"}
			if now {
				args = append(args, "--now")
			}
			copyRun(t, dir, args...)
			copyRun(t, dir, "director", "turn-ended")
			got := waitSessions(t, dir, 2)
			if got[0].Pane != first.Pane || got[1].Pane != first.Pane || len(host.created) != 1 || len(host.closed) != 0 {
				t.Errorf("reload changed presentation: sessions=%+v created=%+v closed=%+v", got, host.created, host.closed)
			}
			if got[0].EndReason != "reloaded" {
				t.Errorf("old end reason=%s", got[0].EndReason)
			}
		})
	}
}

func TestDirectorTabReplacesAFailedRuntime(t *testing.T) {
	dir := tabCopy(t, "exit 1")
	host := newDirectorTabs()
	host.onRun = func(tab adapter.TabRef, _ string) {
		code, _, stderr := runDirectorCmd(t, supervisorArgv(t, dir, tab)[2:]...)
		if code != 1 || !strings.Contains(stderr, "runtime exited with status 1") {
			t.Errorf("failed runtime exit=%d stderr=%s", code, stderr)
		}
	}
	for i := 0; i < 2; i++ {
		code, out, stderr := runWrapper(t, host, "--tab", "--copy", dir)
		host.runs.Wait()
		if code != 0 {
			t.Fatalf("wrapper exit=%d stdout=%s stderr=%s", code, out, stderr)
		}
	}
	if len(host.closed) != 1 || host.closed[0] != host.tabs[0] {
		t.Errorf("failed tab not replaced: closed=%+v tabs=%+v", host.closed, host.tabs)
	}
}

func TestDirectorTabCleanupLeavesUnprovenTabs(t *testing.T) {
	for _, kind := range []string{"busy", "foreign-session", "foreign-workspace", "unrecorded", "missing"} {
		t.Run(kind, func(t *testing.T) {
			dir := tabCopy(t, "/exit")
			host := newDirectorTabs()
			host.onRun = func(tab adapter.TabRef, _ string) {
				if code, out, stderr := runDirectorCmd(t, supervisorArgv(t, dir, tab)[2:]...); code != 0 {
					t.Errorf("%d %s %s", code, out, stderr)
				}
			}
			code, out, stderr := runWrapper(t, host, "--tab", "--copy", dir)
			host.runs.Wait()
			if code != 0 {
				t.Fatalf("%d %s %s", code, out, stderr)
			}
			tab := host.tabs[0]
			replacement := adapter.TabRef{Session: "lab", WorkspaceID: tab.WorkspaceID, TabID: "replacement", PaneID: "replacement"}
			switch kind {
			case "busy":
				host.busy[tab.PaneID] = -1
			case "foreign-session":
				replacement.Session = "elsewhere"
			case "foreign-workspace":
				replacement.WorkspaceID = "elsewhere"
			case "unrecorded":
				host.tabs = append(host.tabs, adapter.TabRef{Session: "lab", WorkspaceID: tab.WorkspaceID, TabID: "user", PaneID: "user"})
				replacement = tab
			case "missing":
				host.tabs = nil
			}
			before := sessions(t, dir)
			app, _, errout := newTestApp(nil, nil)
			app.retireDirectorTabs(context.Background(), host, filepath.Join(dir, "director.store"), 1, replacement)
			if len(host.closed) != 0 {
				t.Errorf("closed unproven tab: %+v", host.closed)
			}
			if kind == "busy" && !strings.Contains(errout.String(), "idle shell") {
				t.Errorf("missing busy diagnostic: %s", errout)
			}
			after := sessions(t, dir)
			if len(after) != len(before) || after[0] != before[0] {
				t.Errorf("cleanup changed session record")
			}
		})
	}
}
