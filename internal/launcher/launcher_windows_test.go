//go:build windows

package launcher

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/release"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// TestLauncherChildStaysInTheJob: on Windows the launcher runs the release as a child, which stays in the Job Object
// a Director or Artisan launch put the launcher in, so the runtime unit still holds the real process; the child's
// output and exit code come back through the launcher.
func TestLauncherChildStaysInTheJob(t *testing.T) {
	f := newFixture(t)
	f.install("v1.0.0")
	dir := f.copyDir("a", "v1.0.0")
	u, err := artisan.NewUnit("launcher-job-test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = u.Close() }()
	cmd := exec.Command(f.self, "director", "status")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), roleEnv+"=launcher", release.HomeEnv+"="+f.home.Dir, "FAKE_STDIN=1", "FAKE_EXIT=5")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	if _, err := u.Start(cmd); err != nil {
		t.Fatal(err)
	}
	// The fake release blocks reading stdin, so both it and the launcher are live members until stdin closes.
	testenv.WaitFor(t, "the launcher and its release in the unit", func() bool {
		members, err := u.Members()
		return err == nil && len(members) == 2
	})
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	<-u.Exited()
	var r ran
	if err := json.Unmarshal(out.Bytes(), &r); err != nil || r.Release != "v1.0.0" {
		t.Fatalf("release printed %q: %v", out.String(), err)
	}
	if code := cmd.ProcessState.ExitCode(); code != 5 {
		t.Fatalf("launcher exit %d, want the release's 5", code)
	}
}

// TestLauncherSurvivesCtrlBreak: a console Ctrl-C or Ctrl-Break reaches every process in the console's group, so
// the release handles it itself; the launcher ignores it, keeps waiting and exits with the release's own exit code
// rather than dying first and leaving the release behind.
func TestLauncherSurvivesCtrlBreak(t *testing.T) {
	f := newFixture(t)
	f.install("v1.0.0")
	dir := f.copyDir("a", "v1.0.0")
	cmd := exec.Command(f.self, "director", "status")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), roleEnv+"=launcher", release.HomeEnv+"="+f.home.Dir, "FAKE_INTERRUPT=1", "FAKE_EXIT=9")
	// A new process group holds only the launcher and its release, so the event below reaches no one else. A new
	// group has Ctrl-C disabled, so the test sends Ctrl-Break, which Go delivers as os.Interrupt just as it does
	// Ctrl-C.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-done:
		case <-time.After(time.Minute):
			_ = cmd.Process.Kill()
		}
	}()
	out := bufio.NewReader(stdout)
	// The release prints "ready" once it listens for the interrupt, and the launcher ignores interrupts before it
	// starts the release, so both are set for the event.
	if line, err := out.ReadString('\n'); err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("release said %q: %v; stderr %s", line, err, errOut.String())
	}
	if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(cmd.Process.Pid)); err != nil {
		t.Fatalf("send Ctrl-Break: %v", err)
	}
	rest, err := io.ReadAll(out)
	if err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatal(err)
	}
	var r ran
	if err := json.Unmarshal(rest, &r); err != nil || r.Release != "v1.0.0" {
		t.Fatalf("release printed %q after the interrupt: %v; stderr %s", rest, err, errOut.String())
	}
	if code := cmd.ProcessState.ExitCode(); code != 9 {
		t.Fatalf("launcher exit %d after Ctrl-Break, want the release's 9; stderr %s", code, errOut.String())
	}
}
