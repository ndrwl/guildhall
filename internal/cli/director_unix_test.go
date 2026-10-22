//go:build unix

package cli

import (
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/baseline"
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
	go func() { done <- app.Run([]string{"director", "start", "--foreground", "--copy", dir}) }()
	var code int
	deadline := time.After(testenv.Ceiling(time.Minute))
	for ended := false; !ended; {
		select {
		case code = <-done:
			ended = true
		case <-time.After(100 * time.Millisecond):
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
