//go:build !windows

package launch

import (
	"os/exec"
	"syscall"
)

// detach starts cmd in its own session, so the caller's terminal and process
// group signals do not reach the attempt's launcher.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
