//go:build windows

package launcher

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"unsafe"

	"golang.org/x/sys/windows"
)

// run starts the release at binary as a child in the same console, with args, env and the launcher's standard
// streams, and returns its exit code. Ctrl-C reaches the child from the console itself, so the launcher ignores it
// and waits for the child's own exit. The child inherits the launcher's Job Object, the runtime unit a Director or
// Artisan launch recorded, so that unit still captures the real process; run checks it did and, should it not
// have, kills the child rather than leave it outside the unit.
func run(binary string, args, env []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	defer signal.Stop(signals)
	cmd := exec.Command(binary, args...)
	cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = env, stdin, stdout, stderr
	if err := cmd.Start(); err != nil {
		return 1, err
	}
	if err := sameJob(cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return 1, err
	}
	err := cmd.Wait()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}

var procIsProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

// isProcessInJob is kernel32's IsProcessInJob for any job.
func isProcessInJob(h windows.Handle, result *bool) error {
	var in int32
	r, _, err := procIsProcessInJob.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&in)))
	if r == 0 {
		return err
	}
	*result = in != 0
	return nil
}

// sameJob refuses a child outside the launcher's Job Object when the launcher is in one.
func sameJob(pid int) error {
	var inJob bool
	if err := isProcessInJob(windows.CurrentProcess(), &inJob); err != nil || !inJob {
		return nil
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("open release process %d: %w", pid, err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var childInJob bool
	if err := isProcessInJob(h, &childInJob); err != nil {
		return fmt.Errorf("release process %d job: %w", pid, err)
	}
	if !childInJob {
		return fmt.Errorf("release process %d left the launcher's Job Object", pid)
	}
	return nil
}
