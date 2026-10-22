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
	id, pgid, isZombie, err := readProcess(pid)
	if err == nil && isZombie {
		return Identity{}, 0, fmt.Errorf("pid %d: %w", pid, errNoProcess)
	}
	return id, pgid, err
}

// lookupExited returns the identity of pid, live or exited and unreaped.
func lookupExited(pid int) (Identity, error) {
	id, _, _, err := readProcess(pid)
	return id, err
}

// readProcess returns the identity and process group of pid and whether it
// is a zombie; a pid with no process is errNoProcess.
func readProcess(pid int) (id Identity, pgid int, isZombie bool, err error) {
	// An absent PID produces an empty result, not a single kinfo_proc.
	// The single-record wrapper turns that valid empty result into EIO.
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if errors.Is(err, unix.ESRCH) || err == nil && (len(kps) == 0 || int(kps[0].Proc.P_pid) != pid) {
		return Identity{}, 0, false, fmt.Errorf("pid %d: %w", pid, errNoProcess)
	}
	if err != nil {
		return Identity{}, 0, false, err
	}
	kp := kps[0]
	t := kp.Proc.P_starttime
	return Identity{PID: pid, Token: fmt.Sprintf("%d.%06d", t.Sec, t.Usec)}, int(kp.Eproc.Pgid), kp.Proc.P_stat == zombie, nil
}
