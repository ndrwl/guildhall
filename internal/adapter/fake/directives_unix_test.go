//go:build unix

package fake

import (
	"syscall"
	"testing"
	"time"
)

func TestIgnoreInterrupt(t *testing.T) {
	for _, ignore := range []bool{false, true} {
		prompt := "guildhall-fake: result=never\nguildhall-fake: lifetime=20s\n"
		if ignore {
			prompt += "guildhall-fake: ignore-interrupt=true\n"
		}
		args := setup(t, prompt)
		p := startFake(t, args)
		// Interrupt only once the fake has started work, past its signal setup.
		time.Sleep(300 * time.Millisecond)
		if err := p.cmd.Process.Signal(syscall.SIGINT); err != nil {
			t.Fatal(err)
		}
		select {
		case <-p.done:
			if ignore {
				t.Error("a fake ignoring interrupts exited on SIGINT")
			}
		case <-time.After(time.Second):
			if !ignore {
				t.Error("a fake did not exit on SIGINT")
			}
		}
	}
}
