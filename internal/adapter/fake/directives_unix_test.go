//go:build unix

package fake

import (
	"syscall"
	"testing"
	"time"
)

func TestIgnoreInterrupt(t *testing.T) {
	for _, ignore := range []bool{false, true} {
		inv := t.TempDir()
		prompt := "guildhall-fake: result=never\nguildhall-fake: lifetime=1h\nguildhall-fake: inventory=" + inv + "\n"
		if ignore {
			prompt += "guildhall-fake: ignore-interrupt=true\n"
		}
		args := setup(t, prompt)
		p := startFake(t, args)
		recorded(t, inv, RoleWorker, 1)
		if err := p.cmd.Process.Signal(syscall.SIGINT); err != nil {
			t.Fatal(err)
		}
		bound := deadline
		if ignore {
			bound = time.Second
		} // timing:allow negative observation only; ignored signal must leave the acknowledged worker alive.
		select {
		case <-p.done:
			if ignore {
				t.Error("a fake ignoring interrupts exited on SIGINT")
			}
		case <-time.After(bound):
			if !ignore {
				t.Error("a fake did not exit on SIGINT")
			}
		}
	}
}
