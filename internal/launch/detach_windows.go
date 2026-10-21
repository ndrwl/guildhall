package launch

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// detach starts cmd in its own process group, so the caller's console
// Ctrl+C and Ctrl+Break do not reach the attempt's launcher.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}
