//go:build unix

package steward

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureSCPCancellation(cmd *exec.Cmd) {
	// scp launches ssh. Kill both on cancellation, not just the scp parent.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
