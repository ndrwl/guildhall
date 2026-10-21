//go:build unix

package claudetest

import (
	"os"
	"time"
)

// parentGone is closed once the stub's parent has exited: the stub has been
// reparented.
func parentGone() <-chan struct{} {
	gone := make(chan struct{})
	ppid := os.Getppid()
	go func() {
		for os.Getppid() == ppid {
			time.Sleep(50 * time.Millisecond)
		}
		close(gone)
	}()
	return gone
}
