package artisan

import (
	"os"

	"golang.org/x/sys/windows"
)

// interruptGraceful is whether Interrupt is a dependable graceful rung:
// CTRL_BREAK reaches only a worker sharing the console and may end it
// abruptly, so it is not.
const interruptGraceful = false

func lockFile(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, new(windows.Overlapped))
}
