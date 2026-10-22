//go:build unix

package artisan

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// groupUnit contains the worker in its own process group. The leader is not
// reaped before Close, so its PID, the group id, stays allocated while the
// unit can signal the group.
type groupUnit struct {
	mu          sync.Mutex
	cmd         *exec.Cmd
	worker      Identity
	foreground  bool
	closed      bool
	done        chan struct{}
	containment Containment
	platformUnit

	// lookup and signal are the leader check's and the rungs' OS calls.
	lookup      func(pid int) (Identity, error)
	lookupGroup func(pid int) (Identity, int, error)
	signal      func(pid int, sig unix.Signal) error
}

func newGroupUnit(c Containment) *groupUnit {
	return &groupUnit{done: make(chan struct{}), containment: c, lookup: Lookup, lookupGroup: lookupGroup, signal: unix.Kill}
}

func (u *groupUnit) Containment() Containment { return u.containment }

// newSessionUnit is newUnit: a worker whose stdin is the caller's terminal already becomes its foreground group.
func newSessionUnit(name string) (Unit, error) { return newUnit(name) }

// containedGroup lists the live members of the process group worker led.
// A group id is not reused while the group has members, so a live process at
// it that is not worker means the group is gone.
func containedGroup(worker Identity) ([]Identity, error) {
	leader, err := Lookup(worker.PID)
	switch {
	case err == nil && leader != worker:
		return nil, nil
	case err != nil && !errors.Is(err, errNoProcess):
		return nil, err
	}
	return groupMembers(worker.PID)
}

func isTerminal(fd int) bool {
	_, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	return err == nil
}

func (u *groupUnit) Start(cmd *exec.Cmd) (Identity, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	switch {
	case u.closed:
		return Identity{}, ErrClosed
	case u.cmd != nil:
		return Identity{}, ErrStarted
	}
	attr := &syscall.SysProcAttr{Setpgid: true}
	if cmd.Stdin == os.Stdin && isTerminal(0) {
		// The worker becomes the terminal's foreground group, so Ctrl+C from
		// the pane reaches it and not the launcher.
		attr.Foreground, attr.Ctty = true, 0
	}
	setParentDeath(attr)
	release, err := u.placeWorker(attr)
	if err != nil {
		return Identity{}, err
	}
	cmd.SysProcAttr = attr
	err = startLocked(cmd)
	release()
	if err != nil {
		if attr.Foreground {
			reclaimTerminal()
		}
		return Identity{}, err
	}
	pid := cmd.Process.Pid
	u.cmd, u.worker, u.foreground = cmd, Identity{PID: pid}, attr.Foreground
	go func() {
		waitExit(pid)
		close(u.done)
	}()
	id, pgid, err := u.lookupGroup(pid)
	switch {
	case errors.Is(err, errNoProcess):
		// The worker exited before the lookup. Setpgid made it lead group
		// pid, and it is unreaped until Close, so it is recorded and its exit
		// is read as any exit is.
		pgid = pid
		id, err = lookupExited(pid)
	case err == nil:
		err = u.verifyWorker(pid)
	}
	if err != nil || pgid != pid {
		if u.foreground {
			reclaimTerminal()
			u.foreground = false
		}
		return Identity{}, errors.Join(fmt.Errorf("%w: worker %d in group %d: %v", ErrContainment, pid, pgid, err), u.closeLocked())
	}
	u.worker = id
	return id, nil
}

func (u *groupUnit) Members() ([]Identity, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.usable(); err != nil {
		return nil, err
	}
	return u.members()
}

func (u *groupUnit) usable() error {
	switch {
	case u.closed:
		return ErrClosed
	case u.cmd == nil:
		return ErrNotStarted
	}
	return nil
}

func (u *groupUnit) Interrupt() error { return u.send(unix.SIGINT) }
func (u *groupUnit) Terminate() error { return u.send(unix.SIGTERM) }
func (u *groupUnit) Kill() error      { return u.send(unix.SIGKILL) }

func (u *groupUnit) send(sig unix.Signal) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.usable(); err != nil {
		return err
	}
	return u.signalUnit(sig)
}

// signalGroup signals the group after the leader check: a live process at the
// group id must be the recorded worker, and an unreadable one is not signalled.
func (u *groupUnit) signalGroup(sig unix.Signal) error {
	pgid := u.worker.PID
	leader, err := u.lookup(pgid)
	switch {
	case err == nil && leader != u.worker:
		return fmt.Errorf("%w: group %d leader %v, worker %v", ErrGroupReused, pgid, leader, u.worker)
	case err != nil && !errors.Is(err, errNoProcess):
		return fmt.Errorf("leader check of group %d: %w", pgid, err)
	}
	if err := u.signal(-pgid, sig); err != nil && !errors.Is(err, unix.ESRCH) {
		return err
	}
	return nil
}

func (u *groupUnit) Exited() <-chan struct{} { return u.done }

func (u *groupUnit) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return nil
	}
	return u.closeLocked()
}

func (u *groupUnit) closeLocked() error {
	if u.cmd == nil {
		if err := u.removeContainment(); err != nil {
			return err
		}
		u.closed = true
		return nil
	}
	var err error
	if u.containment.Kind != KindCgroup && u.worker.Token == "" {
		err = u.signal(-u.worker.PID, unix.SIGKILL)
		if errors.Is(err, unix.ESRCH) {
			err = nil
		}
	} else {
		err = u.signalUnit(unix.SIGKILL)
	}
	if err != nil {
		return err
	}
	if u.worker.Token == "" {
		if err := u.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
	}
	<-u.done
	_ = u.cmd.Wait()
	if u.foreground {
		reclaimTerminal()
		u.foreground = false
	}
	if err := u.removeContainment(); err != nil {
		return err
	}
	u.closed = true
	return nil
}

// reclaimTerminal makes the launcher's group the terminal's foreground again
// after the worker group ends, so the launcher's output is not stopped by
// SIGTTOU.
func reclaimTerminal() {
	signal.Ignore(syscall.SIGTTOU)
	defer signal.Reset(syscall.SIGTTOU)
	_ = unix.IoctlSetPointerInt(0, unix.TIOCSPGRP, unix.Getpgrp())
}
