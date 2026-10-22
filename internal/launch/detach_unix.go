//go:build unix && !linux

package launch

import (
	"os/exec"
	"syscall"
)

// startDetached starts the command build returns in its own session, so the caller's terminal and process group
// signals do not reach the attempt's launcher and it leaves a session runtime's process-group unit.
func startDetached(build func() *exec.Cmd) (*exec.Cmd, error) {
	cmd := build()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd, cmd.Start()
}
