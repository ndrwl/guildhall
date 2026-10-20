package artisan

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

const ioctlGetTermios = unix.TCGETS

type platformUnit struct {
	cgroup *cgroup
}

func (u *platformUnit) placeWorker(attr *syscall.SysProcAttr) (func(), error) {
	if u.cgroup != nil {
		return u.cgroup.place(attr)
	}
	return func() {}, nil
}

func (u *platformUnit) verifyWorker(pid int) error {
	if u.cgroup != nil {
		in, err := u.cgroup.holds(pid)
		if err != nil {
			return err
		}
		if !in {
			return errors.New("not in its cgroup")
		}
	}
	return nil
}

func (u *groupUnit) members() ([]Identity, error) {
	if u.cgroup != nil {
		return u.cgroup.members()
	}
	return groupMembers(u.worker.PID)
}

func (u *groupUnit) signalUnit(sig unix.Signal) error {
	if u.cgroup != nil {
		return u.cgroup.signal(sig)
	}
	return u.signalGroup(sig)
}

func (u *platformUnit) removeContainment() error {
	if u.cgroup != nil {
		return u.cgroup.remove()
	}
	return nil
}

// newUnit contains the worker in a child cgroup when detection finds a
// delegated cgroup v2, otherwise in a process group with the launcher as
// child subreaper. A cgroup that cannot be created is an error, not a
// fallback.
func newUnit(name string) (Unit, error) {
	d := detectCgroup(systemCgroupEnv)
	if d.ok {
		return newCgroupUnit(name, d)
	}
	return newSubreaperUnit(d.reason)
}

// setSubreaper makes the launcher process the child subreaper, once.
var setSubreaper = sync.OnceValue(func() error {
	return unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)
})

// newSubreaperUnit is the fallback: a process group whose orphaned escapes
// reparent to the launcher; reason is the detection result that chose it.
func newSubreaperUnit(reason string) (Unit, error) {
	if err := setSubreaper(); err != nil {
		return nil, fmt.Errorf("%w: PR_SET_CHILD_SUBREAPER: %v", ErrContainment, err)
	}
	return newGroupUnit(Containment{Kind: KindSubreaper, Reason: reason}), nil
}

// Escapes lists the live processes that left the worker's process group and
// were orphaned to the launcher, which as child subreaper inherits them
// instead of init. The launcher runs one unit and starts no other children,
// so every such child is attributed to this unit. Escapes are reported, not
// contained: Kill does not reach them. A cgroup unit has none.
func (u *groupUnit) Escapes() ([]Identity, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.usable(); err != nil || u.containment.Kind != KindSubreaper {
		return nil, err
	}
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	self := os.Getpid()
	var out []Identity
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == u.worker.PID {
			continue
		}
		id, err := Lookup(pid)
		if errors.Is(err, errNoProcess) {
			continue
		}
		if err != nil {
			return nil, err
		}
		boot, err := bootID()
		if err != nil {
			return nil, err
		}
		state, ppid, pgrp, start, err := procStat(pid)
		switch {
		case errors.Is(err, fs.ErrNotExist) || errors.Is(err, unix.ESRCH):
			continue
		case err != nil:
			return nil, err
		case id.Token != boot+":"+start || ppid != self || pgrp == u.worker.PID || state == 'Z' || state == 'X':
			continue
		}
		out = append(out, id)
	}
	return out, nil
}

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
