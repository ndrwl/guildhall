//go:build unix

package artisan

import (
	"errors"
	"fmt"
	"os/exec"
	"testing"

	"golang.org/x/sys/unix"
)

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
