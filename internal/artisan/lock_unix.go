//go:build unix

package artisan

import (
	"os"

	"golang.org/x/sys/unix"
)

// interruptGraceful is whether Interrupt is a dependable graceful rung:
// SIGINT reaches the whole group.
const interruptGraceful = true

func lockFile(f *os.File, nonblocking bool) error {
	flags := unix.LOCK_EX
	if nonblocking {
		flags |= unix.LOCK_NB
	}
	return unix.Flock(int(f.Fd()), flags)
}

func lockBusy(err error) bool { return err == unix.EWOULDBLOCK }
