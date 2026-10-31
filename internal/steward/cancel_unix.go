//go:build unix

package steward

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// configureCancellation makes cancelling cmd, an scp or ssh run, kill its whole process group: scp launches ssh,
// and ssh may launch a proxy command, so killing only the parent could leave them holding its pipes.
func configureCancellation(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
