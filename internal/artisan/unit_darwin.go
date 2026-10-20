package artisan

import (
	"errors"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const ioctlGetTermios = unix.TIOCGETA

// newUnit contains the worker in a process group: macOS has no cgroup and no
// subreaper.
func newUnit(string) (Unit, error) { return newGroupUnit(Containment{Kind: KindGroup}), nil }

// setParentDeath: macOS has no parent-death signal.
func setParentDeath(*syscall.SysProcAttr) {}

func startLocked(cmd *exec.Cmd) error { return cmd.Start() }

// waitExit returns once pid has exited, without reaping it: macOS has no
// waitid WNOWAIT in x/sys, so it polls until the process is a zombie.
func waitExit(pid int) {
	for {
		if _, err := Lookup(pid); errors.Is(err, errNoProcess) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// groupMembers lists the live processes in process group pgid.
func groupMembers(pgid int) ([]Identity, error) {
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pgid)
	if err != nil {
		return nil, err
	}
	var out []Identity
	for _, kp := range kps {
		id, pg, err := lookupGroup(int(kp.Proc.P_pid))
		switch {
		case errors.Is(err, errNoProcess):
		case err != nil:
			return nil, err
		case pg == pgid:
			out = append(out, id)
		}
	}
	return out, nil
}
