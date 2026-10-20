package artisan

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// zombie is SZOMB, the kinfo_proc state of an exited, unreaped process.
const zombie = 5

// Lookup returns the identity of the live process pid: its start time. A
// zombie is not live.
func Lookup(pid int) (Identity, error) {
	id, _, err := lookupGroup(pid)
	return id, err
}

func lookupGroup(pid int) (Identity, int, error) {
	// An absent PID produces an empty result, not a single kinfo_proc.
	// The single-record wrapper turns that valid empty result into EIO.
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if errors.Is(err, unix.ESRCH) || err == nil && len(kps) == 0 {
		return Identity{}, 0, fmt.Errorf("pid %d: %w", pid, errNoProcess)
	}
	if err != nil {
		return Identity{}, 0, err
	}
	kp := kps[0]
	if int(kp.Proc.P_pid) != pid || kp.Proc.P_stat == zombie {
		return Identity{}, 0, fmt.Errorf("pid %d: %w", pid, errNoProcess)
	}
	t := kp.Proc.P_starttime
	return Identity{PID: pid, Token: fmt.Sprintf("%d.%06d", t.Sec, t.Usec)}, int(kp.Eproc.Pgid), nil
}
