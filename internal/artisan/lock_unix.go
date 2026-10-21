//go:build unix

package artisan

import (
	"os"

	"golang.org/x/sys/unix"
)

// interruptGraceful is whether Interrupt is a dependable graceful rung:
// SIGINT reaches the whole group.
const interruptGraceful = true

func lockFile(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX) }
