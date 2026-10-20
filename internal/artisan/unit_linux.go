package artisan

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

const ioctlGetTermios = unix.TCGETS

// setParentDeath asks Linux to SIGKILL the worker if the launcher dies.
func setParentDeath(a *syscall.SysProcAttr) { a.Pdeathsig = syscall.SIGKILL }

// startLocked starts cmd on a locked OS thread: Linux sends the parent-death
// signal when the creating thread exits, and Go retires only threads that
// exit locked.
func startLocked(cmd *exec.Cmd) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	return cmd.Start()
}

// waitExit returns once pid has exited, without reaping it (WNOWAIT).
func waitExit(pid int) {
	var info unix.Siginfo
	for unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil) == unix.EINTR {
	}
}

// groupMembers lists the live processes in process group pgid.
func groupMembers(pgid int) ([]Identity, error) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var out []Identity
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		id, pg, err := lookupGroup(pid)
		switch {
		case errors.Is(err, errNoProcess) || errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return nil, err
		case pg == pgid:
			out = append(out, id)
		}
	}
	return out, nil
}
