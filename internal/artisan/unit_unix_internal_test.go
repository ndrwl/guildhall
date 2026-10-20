//go:build unix

package artisan

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCloseRetriesTerminationFailure(t *testing.T) {
	for _, lookupFailure := range []bool{true, false} {
		t.Run(fmt.Sprint(lookupFailure), func(t *testing.T) {
			worker := Identity{PID: 4242, Token: "owned"}
			fail := true
			u := &groupUnit{cmd: &exec.Cmd{}, worker: worker, done: make(chan struct{}),
				lookup: func(int) (Identity, error) {
					if fail && lookupFailure {
						return Identity{}, unix.EPERM
					}
					return worker, nil
				},
				signal: func(int, unix.Signal) error {
					if fail {
						return unix.EPERM
					}
					return nil
				},
			}
			result := make(chan error, 1)
			go func() { result <- u.Close() }()
			select {
			case err := <-result:
				if !errors.Is(err, unix.EPERM) {
					t.Fatalf("Close = %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("Close waited after termination failed")
			}
			if u.closed {
				t.Fatal("failed Close discarded ownership")
			}
			fail = false
			close(u.done)
			if err := u.Close(); err != nil || !u.closed {
				t.Fatalf("retry Close = %v, closed = %v", err, u.closed)
			}
		})
	}
}

func TestStartRollbackKillsDescendants(t *testing.T) {
	unit, err := newUnit("rollback")
	if err != nil {
		t.Fatal(err)
	}
	u := unit.(*groupUnit)
	t.Cleanup(func() { _ = u.Close() })
	pidFile := filepath.Join(t.TempDir(), "child")
	var child Identity
	u.lookupGroup = func(pid int) (Identity, int, error) {
		select {
		case <-u.done:
		case <-time.After(10 * time.Second):
			return Identity{}, 0, errors.New("worker did not exit")
		}
		b, err := os.ReadFile(pidFile)
		if err != nil {
			return Identity{}, 0, err
		}
		childPID, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil {
			return Identity{}, 0, err
		}
		child, err = Lookup(childPID)
		if err != nil {
			return Identity{}, 0, err
		}
		return lookupGroup(pid)
	}
	cmd := exec.Command("/bin/sh", "-c", `sleep 60 & echo $! > "$1"`, "worker", pidFile)
	if _, err := u.Start(cmd); !errors.Is(err, ErrContainment) {
		t.Fatalf("Start = %v", err)
	}
	if child.PID == 0 {
		t.Fatal("child identity was not recorded")
	}
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if lv, _ := Probe(child); lv == Gone {
			return
		}
	}
	t.Fatalf("descendant %v survived startup rollback", child)
}

// TestLeaderCheck: the group is signalled only while the process at the group
// id is the recorded worker or has exited unreaped; anything else is refused
// without a signal.
func TestLeaderCheck(t *testing.T) {
	worker := Identity{PID: 4242, Token: "boot:100"}
	cases := []struct {
		name    string
		leader  Identity
		err     error
		sigErr  error
		want    error
		signals bool
	}{
		{name: "leader is the worker", leader: worker, signals: true},
		{name: "leader exited, unreaped", err: fmt.Errorf("pid 4242: %w", errNoProcess), signals: true},
		{name: "group already empty", leader: worker, sigErr: unix.ESRCH, signals: true},
		{name: "group id reused by another process", leader: Identity{PID: 4242, Token: "boot:999"}, want: ErrGroupReused},
		{name: "leader cannot be read", err: unix.EPERM, want: unix.EPERM},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var sent []int
			u := &groupUnit{cmd: &exec.Cmd{}, worker: worker, done: make(chan struct{}),
				lookup: func(int) (Identity, error) { return c.leader, c.err },
				signal: func(pid int, _ unix.Signal) error { sent = append(sent, pid); return c.sigErr },
			}
			for name, rung := range map[string]func() error{"Interrupt": u.Interrupt, "Terminate": u.Terminate, "Kill": u.Kill} {
				sent = nil
				err := rung()
				if !errors.Is(err, c.want) || (c.want == nil) != (err == nil) {
					t.Errorf("%s() = %v, want %v", name, err, c.want)
				}
				if c.signals && (len(sent) != 1 || sent[0] != -worker.PID) || !c.signals && len(sent) != 0 {
					t.Errorf("%s() signalled %v", name, sent)
				}
			}
		})
	}
}
