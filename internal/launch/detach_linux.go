package launch

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/ndrwl/guildhall/internal/artisan"
)

// startDetached starts the command build returns in its own session, so the caller's terminal and process group
// signals do not reach the attempt's launcher and it leaves a session runtime's process-group unit. Inside a
// session runtime's cgroup unit it is created in the cgroup artisan.DetachCgroupEnv names, outside the unit
// (CLONE_INTO_CGROUP); when that cgroup cannot be opened or entered it is started where the caller is.
func startDetached(build func() *exec.Cmd) (*exec.Cmd, error) {
	if dir := os.Getenv(artisan.DetachCgroupEnv); dir != "" {
		if fd, err := unix.Open(dir, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0); err == nil {
			cmd := build()
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, UseCgroupFD: true, CgroupFD: fd}
			err = cmd.Start()
			_ = unix.Close(fd)
			if err == nil {
				return cmd, nil
			}
		}
	}
	cmd := build()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd, cmd.Start()
}
