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

	"github.com/ndrwl/guildhall/internal/testenv"
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
		case <-time.After(testenv.Ceiling(10 * time.Second)):
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
		// An unreadable worker is not contained; one merely exited would be.
		return Identity{}, 0, unix.EPERM
	}
	cmd := exec.Command("/bin/sh", "-c", `sleep 60 & echo $! > "$1"`, "worker", pidFile)
	if _, err := u.Start(cmd); !errors.Is(err, ErrContainment) {
		t.Fatalf("Start = %v", err)
	}
	if child.PID == 0 {
		t.Fatal("child identity was not recorded")
	}
	for end := time.Now().Add(testenv.Ceiling(10 * time.Second)); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
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

// exitedBeforeLookup is a unit whose start-time lookup runs only once the
// worker has exited, the race a worker that exits at once can win.
func exitedBeforeLookup(t *testing.T) *groupUnit {
	t.Helper()
	unit, err := newUnit("exited-" + strconv.Itoa(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	u := unit.(*groupUnit)
	t.Cleanup(func() { _ = u.Close() })
	u.lookupGroup = func(pid int) (Identity, int, error) {
		select {
		case <-u.done:
		case <-time.After(testenv.Ceiling(10 * time.Second)):
			return Identity{}, 0, errors.New("worker did not exit")
		}
		return lookupGroup(pid)
	}
	return u
}

// TestStartWorkerExitedBeforeLookup: a worker gone before its group is looked
// up has exited, not escaped: Start reports its identity and Exited closes.
func TestStartWorkerExitedBeforeLookup(t *testing.T) {
	u := exitedBeforeLookup(t)
	cmd := exec.Command("true")
	id, err := u.Start(cmd)
	if err != nil {
		t.Fatalf("Start = %v", err)
	}
	if id.PID != cmd.Process.Pid || id.Token == "" {
		t.Fatalf("Start identity = %+v, want pid %d with a token", id, cmd.Process.Pid)
	}
	select {
	case <-u.Exited():
	case <-time.After(testenv.Ceiling(10 * time.Second)):
		t.Fatal("Exited did not close")
	}
	if lv, err := Probe(id); lv != Gone || err != nil {
		t.Fatalf("Probe(%v) = %v, %v; want gone", id, lv, err)
	}
	if err := u.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
}

type noResultFiles struct{}

func (noResultFiles) ValidResult() (bool, error) { return false, nil }
func (noResultFiles) Marked() (bool, error)      { return false, nil }
func (noResultFiles) Mark() error                { return nil }

// TestLauncherWorkerExitedBeforeLookup: the launcher reads that exit as any
// exit, so the attempt fails with no result rather than on containment.
func TestLauncherWorkerExitedBeforeLookup(t *testing.T) {
	l, err := NewLauncher(LauncherConfig{Dir: t.TempDir(), Unit: exitedBeforeLookup(t), Files: noResultFiles{},
		Key: AttemptKey{Repository: "app", TaskID: "t-1", Step: "build", Attempt: 1}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := l.Run(exec.Command("true"), nil)
	if err != nil || out.State != Failed {
		t.Fatalf("Run = %+v, %v; want failed with no error", out, err)
	}
}
