//go:build unix

package cli

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/baseline"
	"github.com/ndrwl/guildhall/internal/adapter/adaptertest"
	"github.com/ndrwl/guildhall/internal/herdr"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/testenv"
	"github.com/ndrwl/guildhall/skills"
)

// TestDirectorStartRecordsATerminatedSessionStopped sends SIGTERM to a supervisor whose session waits on input:
// the session ends, is recorded stopped, and the supervisor exits 1.
func TestDirectorStartRecordsATerminatedSessionStopped(t *testing.T) {
	dir := directorCopy(t, "[director]\nadapter = \"fake\"\n")
	stdin, typing, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close(); _ = typing.Close() }()
	// Held so that a SIGTERM sent before the supervisor handles it does not end the test binary.
	held := make(chan os.Signal, 1)
	signal.Notify(held, syscall.SIGTERM)
	defer signal.Stop(held)

	app, stdout, stderr := newTestApp(nil, nil)
	app.Executable = os.Executable
	app.LookPath = exec.LookPath
	app.InstallSkills = skills.Install
	app.InstallBaseline = baseline.Install
	app.Stdin = stdin
	done := make(chan int, 1)
	go func() { done <- app.Run([]string{"director", "start", "--copy", dir}) }()
	var code int
	deadline := time.After(testenv.Ceiling(time.Minute))
	for ended := false; !ended; {
		select {
		case code = <-done:
			ended = true
		case <-time.After(100 * time.Millisecond): // timing:allow readiness polling within the separate deadline.
			if got := sessions(t, dir); len(got) == 1 && got[0].State == store.DirectorRecovering {
				_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
			}
		case <-deadline:
			t.Fatal("director start did not end after SIGTERM")
		}
	}
	if code != 1 || !strings.Contains(stderr.String(), "terminated") {
		t.Errorf("exit %d, stdout %q, stderr %q; want exit 1 naming the termination", code, stdout, stderr)
	}
	if got := sessions(t, dir); len(got) != 1 || got[0].State != store.DirectorStopped {
		t.Errorf("sessions = %+v; want session 1 stopped", got)
	}
}

// TestDirectorStartRunsTheSessionInAShellPane runs the wrapper over adaptertest.ShellPanes, whose panes run each
// typed line with sh: the quoted line starts the supervisor, which records the pane it was given, and the
// wrapper prints that session.
func TestDirectorStartRunsTheSessionInAShellPane(t *testing.T) {
	dir := tabCopy(t)
	panes := adaptertest.NewShellPanes(t, "lab", "w1")
	code, out, stderr := runWrapper(t, panes, "--tab", "--copy", dir)
	if code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out, stderr)
	}
	d := lastSession(t, out)
	if d.Number != 1 || d.Pane.Session != "lab" || d.Pane.Workspace == "w1" || d.Pane.Tab == "" || d.Pane.Pane == "" {
		t.Errorf("printed %+v; want session 1 in a tab of a new workspace", d)
	}
	ws, err := panes.Workspaces(context.Background())
	if err != nil || !slices.Contains(ws, herdr.Workspace{ID: d.Pane.Workspace, Label: "alpha"}) {
		t.Errorf("workspaces = %+v, %v; want %s labelled alpha", ws, err, d.Pane.Workspace)
	}
	if sent := panes.Sent(); len(sent) != 1 || !strings.Contains(sent[0], "'--herdr-pane'") || strings.Contains(sent[0], "'--tab'") {
		t.Errorf("sent = %q", sent)
	}
}
